import threading
from datetime import datetime, timezone


class ModelCache:
    """Thread-safe store of trained models keyed by (metric, instance)."""

    def __init__(self):
        self._lock = threading.Lock()
        self._entries: dict[tuple[str, str], tuple[object, str]] = {}

    def put(self, metric: str, instance: str | None, model) -> str:
        """Cache model and return its training timestamp (ISO 8601, UTC)."""
        trained_at = datetime.now(timezone.utc).isoformat()
        with self._lock:
            self._entries[(metric, instance or "")] = (model, trained_at)
        return trained_at

    def get(self, metric: str, instance: str | None):
        """Return (model, trained_at), or None when no model is cached."""
        with self._lock:
            return self._entries.get((metric, instance or ""))

    def __len__(self) -> int:
        with self._lock:
            return len(self._entries)
