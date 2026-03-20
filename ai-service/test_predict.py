"""
Quick smoke test for the AI service.

Generates 14 days of synthetic CPU data (daily sine wave + noise),
sends it to /predict, and prints the forecast.

Usage:
    1. Start the server:  uvicorn main:app --port 5000
    2. Run this script:   python test_predict.py
"""

import json
import requests
import numpy as np
from datetime import datetime, timedelta, timezone


def generate_synthetic_cpu_data(days: int = 14, interval_minutes: int = 5) -> list[dict]:
    """
    Generate realistic-looking CPU usage data.

    Pattern: ~30% base + daily sine wave (peak ~60% at 2pm, trough ~20% at 3am) + noise.
    """
    points = []
    start = datetime.now(timezone.utc) - timedelta(days=days)
    total_points = (days * 24 * 60) // interval_minutes

    for i in range(total_points):
        t = start + timedelta(minutes=i * interval_minutes)
        hour_of_day = t.hour + t.minute / 60.0

        # Daily pattern: sine wave peaking at 14:00 (2pm)
        daily_component = 15 * np.sin(2 * np.pi * (hour_of_day - 8) / 24)

        # Base load + weekly pattern (slightly lower on weekends)
        weekday = t.weekday()
        weekend_factor = 0.8 if weekday >= 5 else 1.0

        base = 30 * weekend_factor
        noise = np.random.normal(0, 3)

        cpu = max(0, min(100, base + daily_component + noise))
        points.append({"ds": t.isoformat(), "y": round(cpu, 2)})

    return points


def main():
    print("Generating 14 days of synthetic CPU data...")
    history = generate_synthetic_cpu_data(days=14, interval_minutes=5)
    print(f"Generated {len(history)} data points")
    print(f"  Range: {history[0]['ds']} → {history[-1]['ds']}")

    payload = {
        "metric_name": "cpu",
        "history": history,
        "horizon_minutes": 60,
    }

    print("\nSending to AI service at http://localhost:5000/predict ...")
    try:
        resp = requests.post("http://localhost:5000/predict", json=payload, timeout=120)
        resp.raise_for_status()
    except requests.ConnectionError:
        print("ERROR: Cannot connect. Is the server running? (uvicorn main:app --port 5000)")
        return
    except requests.HTTPError as e:
        print(f"ERROR: {e}")
        print(resp.text)
        return

    result = resp.json()
    print(f"\n{'='*60}")
    print(f"Metric: {result['metric_name']}")
    print(f"Horizon: {result['horizon_minutes']} minutes")
    print(f"Predictions ({len(result['predictions'])} points):")
    print(f"{'='*60}")

    for p in result["predictions"]:
        print(f"  {p['time']}  →  {p['value']:6.2f}%  (range: {p['lower']:.2f} – {p['upper']:.2f})")

    print(f"\n✅ AI service is working correctly!")


if __name__ == "__main__":
    main()
