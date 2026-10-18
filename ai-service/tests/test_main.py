import pytest
from fastapi.testclient import TestClient

import main


@pytest.fixture
def client():
    with TestClient(main.app) as c:
        yield c


def test_health_before_any_training(client, monkeypatch):
    monkeypatch.setattr(main, "model_has_trained", False)

    resp = client.get("/health")

    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "model_trained": False}


def test_predict_rejects_short_history(client):
    resp = client.post(
        "/predict",
        json={"metric_name": "cpu", "history": [{"ds": "2026-01-01T00:00:00Z", "y": 1}]},
    )

    assert resp.status_code == 400


def test_predict_returns_forecast_for_utc_history(client, monkeypatch, history):
    monkeypatch.setattr(main, "model_has_trained", False)

    resp = client.post(
        "/predict",
        json={"metric_name": "memory", "history": history, "horizon_minutes": 30},
    )

    assert resp.status_code == 200
    body = resp.json()
    assert body["metric_name"] == "memory"
    assert body["horizon_minutes"] == 30
    assert len(body["predictions"]) == 6
    assert main.model_has_trained is True


@pytest.mark.parametrize("horizon", [0, -5, 10081])
def test_predict_rejects_out_of_range_horizon(client, history, horizon):
    resp = client.post(
        "/predict",
        json={"metric_name": "cpu", "history": history[:2], "horizon_minutes": horizon},
    )

    assert resp.status_code == 422
