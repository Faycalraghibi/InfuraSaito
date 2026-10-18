import math
from datetime import datetime, timedelta, timezone

import numpy as np
import pandas as pd
import pytest

from model import ForecastModel


def noisy_history(days: int = 3, seed: int = 0) -> list[dict]:
    """5-minute points: a daily sine plus deterministic Gaussian noise (sigma 1)."""
    rng = np.random.default_rng(seed)
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    return [
        {
            "ds": (start + timedelta(minutes=5 * i)).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "y": 30 + 10 * math.sin(2 * math.pi * i / 288) + float(rng.normal(0, 1)),
        }
        for i in range(days * 288)
    ]


@pytest.fixture(scope="module")
def trained():
    np.random.seed(0)  # Prophet's uncertainty sampling uses numpy's global RNG
    history = noisy_history()
    model = ForecastModel()
    model.train(history)
    return model, history


def test_detect_before_train_raises():
    with pytest.raises(RuntimeError):
        ForecastModel().detect_anomalies([{"ds": "2026-01-01T00:00:00Z", "y": 1.0}])


@pytest.mark.parametrize("spike", [30, -30])
def test_detect_anomalies_flags_only_the_spike(trained, spike):
    model, history = trained
    recent = [dict(p) for p in history[-24:]]
    recent[10]["y"] += spike

    np.random.seed(1)
    anomalies, checked = model.detect_anomalies(recent)

    assert checked == 24
    assert [a["time"] for a in anomalies] == [pd.Timestamp(recent[10]["ds"]).isoformat()]
    a = anomalies[0]
    assert a["value"] == round(recent[10]["y"], 2)
    assert a["lower"] <= a["expected"] <= a["upper"]
    assert (a["deviation"] > 20) if spike > 0 else (a["deviation"] < -20)


def test_detect_anomalies_on_normal_data_flags_nothing(trained):
    model, history = trained

    np.random.seed(2)
    anomalies, checked = model.detect_anomalies(history[-48:])

    assert checked == 48
    assert anomalies == []


def test_detect_anomalies_skips_unparseable_points(trained):
    model, history = trained

    anomalies, checked = model.detect_anomalies(history[-3:] + [{"ds": "not-a-time", "y": 1.0}])

    assert checked == 3
