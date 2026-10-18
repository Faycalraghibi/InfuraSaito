import logging
from statistics import NormalDist

from prophet import Prophet
import pandas as pd

logger = logging.getLogger(__name__)


def _prepare(history: list[dict]) -> pd.DataFrame:
    df = pd.DataFrame(history)
    # Prophet rejects timezone-aware timestamps: parse as UTC, then drop the zone.
    # Unparseable timestamps become NaT and are removed by dropna below.
    df["ds"] = pd.to_datetime(df["ds"], utc=True, format="ISO8601", errors="coerce").dt.tz_localize(None)
    df["y"] = df["y"].astype(float)
    return df.dropna(subset=["ds", "y"])


class ForecastModel:
    def __init__(self):
        self.model: Prophet | None = None
        self.is_trained: bool = False

    def train(self, history: list[dict]) -> dict:
        df = _prepare(history)

        if len(df) < 2:
            raise ValueError(f"Need at least 2 data points, got {len(df)}")

        logger.info("Training Prophet on %d data points", len(df))

        self.model = Prophet(
            daily_seasonality=True,
            weekly_seasonality=True,
            yearly_seasonality=False,  # Not useful for infra metrics at MVP scale
            changepoint_prior_scale=0.05,  # Conservative to resist overfitting
        )
        self.model.fit(df)
        self.is_trained = True

        logger.info("Training complete")
        return {
            "status": "trained",
            "data_points": len(df),
            "date_range": {
                "start": df["ds"].min().isoformat(),
                "end": df["ds"].max().isoformat(),
            },
        }

    def predict(self, horizon_minutes: int, freq: str = "5min") -> list[dict]:
        if not self.is_trained or self.model is None:
            raise RuntimeError("Model is not trained. Call train() first.")

        periods = max(1, horizon_minutes // int(pd.Timedelta(freq).total_seconds() // 60))

        future = self.model.make_future_dataframe(periods=periods, freq=freq)
        forecast = self.model.predict(future)

        # Only return the future predictions, not the fitted historical values
        last_training_time = self.model.history["ds"].max()
        future_forecast = forecast[forecast["ds"] > last_training_time]

        predictions = []
        for _, row in future_forecast.iterrows():
            predictions.append(
                {
                    "time": row["ds"].tz_localize("UTC").isoformat(),
                    "value": round(float(row["yhat"]), 2),
                    "lower": round(float(row["yhat_lower"]), 2),
                    "upper": round(float(row["yhat_upper"]), 2),
                }
            )

        logger.info("Generated %d predictions for horizon=%d min", len(predictions), horizon_minutes)
        return predictions

    def detect_anomalies(self, history: list[dict], sigmas: float = 3.0) -> tuple[list[dict], int]:
        """Return the points more than `sigmas` standard deviations from the model's
        expected value, and how many points were checked.

        Prophet's uncertainty band is an interval_width (default 80%) interval, so flagging
        every point outside it would flag about 1 in 5 normal points. Instead the standard
        deviation is estimated from the band's width.
        """
        if not self.is_trained or self.model is None:
            raise RuntimeError("Model is not trained. Call train() first.")

        # Prophet returns predictions sorted by ds; sort the input the same way to align rows.
        df = _prepare(history).sort_values("ds").reset_index(drop=True)
        if df.empty:
            return [], 0

        z = NormalDist().inv_cdf(0.5 + self.model.interval_width / 2)
        fitted = self.model.predict(df[["ds"]])

        anomalies = []
        for y, row in zip(df["y"], fitted.itertuples()):
            sigma = (row.yhat_upper - row.yhat_lower) / (2 * z)
            deviation = y - row.yhat
            if sigma > 0 and abs(deviation) > sigmas * sigma:
                anomalies.append(
                    {
                        "time": row.ds.tz_localize("UTC").isoformat(),
                        "value": round(float(y), 2),
                        "expected": round(float(row.yhat), 2),
                        "lower": round(float(row.yhat_lower), 2),
                        "upper": round(float(row.yhat_upper), 2),
                        "deviation": round(float(deviation), 2),
                    }
                )

        logger.info("Checked %d points, found %d anomalies", len(df), len(anomalies))
        return anomalies, len(df)
