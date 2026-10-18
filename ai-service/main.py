import logging
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from model import ForecastModel

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)

app = FastAPI(title="InfuraSaito AI Forecasting Service", version="0.1.0")

# Set once any request has trained successfully; reported by /health.
# Each request builds its own ForecastModel so concurrent calls never share state.
model_has_trained = False


class DataPoint(BaseModel):
    ds: str = Field(..., description="ISO 8601 timestamp")
    y: float = Field(..., description="Metric value")


class PredictRequest(BaseModel):
    metric_name: str = Field(..., description="Name of the metric (e.g. 'cpu')")
    instance: str | None = Field(default=None, description="Instance the history belongs to")
    history: list[DataPoint] = Field(..., description="Historical data points")
    horizon_minutes: int = Field(
        default=60, gt=0, le=10080, description="How far ahead to predict (minutes, at most 7 days)"
    )


class PredictionPoint(BaseModel):
    time: str
    value: float
    lower: float
    upper: float


class PredictResponse(BaseModel):
    metric_name: str
    instance: str | None = None
    horizon_minutes: int
    predictions: list[PredictionPoint]


class HealthResponse(BaseModel):
    status: str
    model_trained: bool


@app.get("/health", response_model=HealthResponse)
def health():
    return HealthResponse(status="ok", model_trained=model_has_trained)


@app.post("/predict", response_model=PredictResponse)
def predict(req: PredictRequest):
    """
    Accepts historical metric data, trains Prophet, and returns predictions.

    For MVP, the model is retrained on every call. This is simple and correct
    for a demo. In production, training and inference would be decoupled.
    """
    global model_has_trained

    if len(req.history) < 2:
        raise HTTPException(status_code=400, detail="Need at least 2 data points in history")

    try:
        history_dicts = [{"ds": dp.ds, "y": dp.y} for dp in req.history]
        forecast_model = ForecastModel()
        forecast_model.train(history_dicts)
        predictions = forecast_model.predict(req.horizon_minutes)
        model_has_trained = True
    except Exception as e:
        logger.error("Prediction failed: %s", str(e), exc_info=True)
        raise HTTPException(status_code=500, detail=f"Prediction failed: {str(e)}")

    return PredictResponse(
        metric_name=req.metric_name,
        instance=req.instance,
        horizon_minutes=req.horizon_minutes,
        predictions=[PredictionPoint(**p) for p in predictions],
    )
