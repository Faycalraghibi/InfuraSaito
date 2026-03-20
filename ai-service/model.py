import logging
from prophet import Prophet
import pandas as pd

logger = logging.getLogger(__name__)

class ForecastModel:
    def __init__(self):
        self.model: Prophet | None = None
        self.is_trained: bool = False

    def train(self, history: list[dict]) -> dict:
        df = pd.DataFrame(history)
        df["ds"] = pd.to_datetime(df["ds"])
        df["y"] = df["y"].astype(float)

        df = df.dropna(subset=["ds", "y"])

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
                    "time": row["ds"].isoformat(),
                    "value": round(float(row["yhat"]), 2),
                    "lower": round(float(row["yhat_lower"]), 2),
                    "upper": round(float(row["yhat_upper"]), 2),
                }
            )

        logger.info("Generated %d predictions for horizon=%d min", len(predictions), horizon_minutes)
        return predictions
