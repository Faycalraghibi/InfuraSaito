import threading

from model_cache import ModelCache


def test_get_missing_returns_none():
    assert ModelCache().get("cpu", "a:9100") is None


def test_put_then_get_returns_model_and_timestamp():
    cache = ModelCache()
    model = object()

    trained_at = cache.put("cpu", "a:9100", model)

    assert cache.get("cpu", "a:9100") == (model, trained_at)
    assert trained_at.endswith("+00:00")


def test_keys_are_per_metric_and_instance():
    cache = ModelCache()
    cache.put("cpu", "a:9100", object())

    assert cache.get("cpu", "b:9100") is None
    assert cache.get("memory", "a:9100") is None


def test_none_and_empty_instance_share_a_key():
    cache = ModelCache()
    model = object()
    cache.put("cpu", None, model)

    assert cache.get("cpu", "")[0] is model


def test_concurrent_puts_and_gets():
    cache = ModelCache()

    def worker(i):
        for _ in range(200):
            cache.put("cpu", f"i{i}", object())
            assert cache.get("cpu", f"i{i}") is not None

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert len(cache) == 8
