import pandas as pd
import pytest

from model import ForecastModel


def test_predict_before_train_raises():
    with pytest.raises(RuntimeError):
        ForecastModel().predict(60)


def test_train_rejects_fewer_than_two_points():
    with pytest.raises(ValueError, match="at least 2"):
        ForecastModel().train([{"ds": "2026-01-01T00:00:00", "y": 1.0}])


def test_train_on_naive_timestamps(history):
    naive = [{"ds": p["ds"].rstrip("Z"), "y": p["y"]} for p in history]
    result = ForecastModel().train(naive)
    assert result["data_points"] == len(naive)


def test_train_accepts_utc_timestamps_from_go(history):
    result = ForecastModel().train(history)
    assert result["data_points"] == len(history)


def test_predict_returns_only_future_points_in_utc(history):
    model = ForecastModel()
    model.train(history)

    preds = model.predict(60)

    assert len(preds) == 12  # 60 min / 5 min
    last_seen = pd.Timestamp(history[-1]["ds"])
    for p in preds:
        t = pd.Timestamp(p["time"])
        assert t.tzinfo is not None, "times must carry a UTC offset"
        assert t > last_seen
        assert p["lower"] <= p["value"] <= p["upper"]
