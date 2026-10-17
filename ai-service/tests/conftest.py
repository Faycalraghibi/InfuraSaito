import math
from datetime import datetime, timedelta, timezone

import pytest


@pytest.fixture
def history() -> list[dict]:
    """3 days of 5-minute points with a daily sine pattern, timestamped like the Go API sends them."""
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    return [
        {
            "ds": (start + timedelta(minutes=5 * i)).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "y": 30 + 10 * math.sin(2 * math.pi * i / 288),
        }
        for i in range(3 * 288)
    ]
