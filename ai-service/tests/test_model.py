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
