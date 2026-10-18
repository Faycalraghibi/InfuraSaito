import logging
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from model import ForecastModel
from model_cache import ModelCache

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)

app = FastAPI(title="InfuraSaito AI Forecasting Service", version="0.1.0")

# Set once any request has trained successfully; reported by /health.
# Each request builds its own ForecastModel so concurrent calls never share state.
model_has_trained = False

# Trained models keyed by (metric, instance), filled by /train and read by /forecast.
model_cache = ModelCache()


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


class TrainRequest(BaseModel):
    metric_name: str = Field(..., description="Name of the metric (e.g. 'cpu')")
    instance: str | None = Field(default=None, description="Instance the history belongs to")
    history: list[DataPoint] = Field(..., description="Historical data points")


class TrainResponse(BaseModel):
    metric_name: str
    instance: str | None = None
    data_points: int
    trained_at: str


class ForecastRequest(BaseModel):
    metric_name: str
    instance: str | None = None
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
    trained_at: str | None = None


class HealthResponse(BaseModel):
    status: str
    model_trained: bool
    cached_models: int


@app.get("/health", response_model=HealthResponse)
def health():
    return HealthResponse(status="ok", model_trained=model_has_trained, cached_models=len(model_cache))


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


@app.post("/train", response_model=TrainResponse)
def train(req: TrainRequest):
    """Fit a model on the history and cache it for /forecast."""
    global model_has_trained

    if len(req.history) < 2:
        raise HTTPException(status_code=400, detail="Need at least 2 data points in history")

    forecast_model = ForecastModel()
    try:
        result = forecast_model.train([{"ds": dp.ds, "y": dp.y} for dp in req.history])
    except Exception as e:
        logger.error("Training failed: %s", str(e), exc_info=True)
        raise HTTPException(status_code=500, detail=f"Training failed: {str(e)}")

    trained_at = model_cache.put(req.metric_name, req.instance, forecast_model)
    model_has_trained = True
    return TrainResponse(
        metric_name=req.metric_name,
        instance=req.instance,
        data_points=result["data_points"],
        trained_at=trained_at,
    )


@app.post("/forecast", response_model=PredictResponse)
def forecast(req: ForecastRequest):
    """Predict from the cached model; 404 when /train has not run for this metric and instance."""
    entry = model_cache.get(req.metric_name, req.instance)
    if entry is None:
        raise HTTPException(status_code=404, detail="No trained model for this metric and instance")
    forecast_model, trained_at = entry

    try:
        predictions = forecast_model.predict(req.horizon_minutes)
    except Exception as e:
        logger.error("Forecast failed: %s", str(e), exc_info=True)
        raise HTTPException(status_code=500, detail=f"Forecast failed: {str(e)}")

    return PredictResponse(
        metric_name=req.metric_name,
        instance=req.instance,
        horizon_minutes=req.horizon_minutes,
        predictions=[PredictionPoint(**p) for p in predictions],
        trained_at=trained_at,
    )
