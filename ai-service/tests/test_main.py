import pytest
from fastapi.testclient import TestClient

import main
from model_cache import ModelCache


@pytest.fixture(autouse=True)
def fresh_cache(monkeypatch):
    monkeypatch.setattr(main, "model_cache", ModelCache())


@pytest.fixture
def client():
    with TestClient(main.app) as c:
        yield c


def test_health_before_any_training(client, monkeypatch):
    monkeypatch.setattr(main, "model_has_trained", False)

    resp = client.get("/health")

    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "model_trained": False, "cached_models": 0}


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


@pytest.mark.parametrize("instance", ["node-a:9100", None])
def test_predict_echoes_instance(client, history, instance):
    payload = {"metric_name": "cpu", "history": history, "horizon_minutes": 30}
    if instance is not None:
        payload["instance"] = instance

    resp = client.post("/predict", json=payload)

    assert resp.status_code == 200
    assert resp.json()["instance"] == instance


def test_forecast_without_trained_model_returns_404(client):
    resp = client.post("/forecast", json={"metric_name": "cpu", "horizon_minutes": 30})

    assert resp.status_code == 404


def test_train_then_forecast_uses_cached_model(client, history):
    train = client.post("/train", json={"metric_name": "cpu", "instance": "a:9100", "history": history})

    assert train.status_code == 200
    assert train.json()["data_points"] == len(history)

    resp = client.post("/forecast", json={"metric_name": "cpu", "instance": "a:9100", "horizon_minutes": 30})

    assert resp.status_code == 200
    body = resp.json()
    assert body["metric_name"] == "cpu"
    assert body["instance"] == "a:9100"
    assert len(body["predictions"]) == 6
    assert body["trained_at"] == train.json()["trained_at"]


def test_models_are_cached_per_instance(client, history):
    client.post("/train", json={"metric_name": "cpu", "instance": "a:9100", "history": history})

    resp = client.post("/forecast", json={"metric_name": "cpu", "instance": "b:9100", "horizon_minutes": 30})

    assert resp.status_code == 404


def test_train_rejects_short_history(client):
    resp = client.post(
        "/train",
        json={"metric_name": "cpu", "history": [{"ds": "2026-01-01T00:00:00Z", "y": 1}]},
    )

    assert resp.status_code == 400


@pytest.mark.parametrize("horizon", [0, 10081])
def test_forecast_rejects_out_of_range_horizon(client, horizon):
    resp = client.post("/forecast", json={"metric_name": "cpu", "horizon_minutes": horizon})

    assert resp.status_code == 422


def test_health_reports_cached_models(client, history):
    client.post("/train", json={"metric_name": "cpu", "history": history})

    body = client.get("/health").json()

    assert body["cached_models"] == 1
    assert body["model_trained"] is True


def test_anomalies_without_trained_model_returns_404(client, history):
    resp = client.post("/anomalies", json={"metric_name": "cpu", "history": history[-12:]})

    assert resp.status_code == 404


def test_anomalies_rejects_empty_history(client):
    resp = client.post("/anomalies", json={"metric_name": "cpu", "history": []})

    assert resp.status_code == 422


def test_anomalies_uses_the_cached_model(client, history):
    train = client.post("/train", json={"metric_name": "cpu", "instance": "a:9100", "history": history})
    recent = [dict(p) for p in history[-12:]]
    recent[5]["y"] += 50

    resp = client.post("/anomalies", json={"metric_name": "cpu", "instance": "a:9100", "history": recent})

    assert resp.status_code == 200
    body = resp.json()
    assert body["metric_name"] == "cpu"
    assert body["instance"] == "a:9100"
    assert body["checked"] == 12
    assert body["trained_at"] == train.json()["trained_at"]
    spike_time = recent[5]["ds"].replace("Z", "+00:00")
    assert spike_time in [a["time"] for a in body["anomalies"]]
