"""Generates infra/grafana/dashboards/infurasaito.json.

Edit this script rather than the JSON, then run: python3 infra/grafana/gen_dashboard.py
"""
import json
import sys
from pathlib import Path

OUTPUT = Path(__file__).parent / "dashboards" / "infurasaito.json"

DS = {"type": "prometheus", "uid": "prometheus"}

# Instant-style PromQL per metric, matching go-api/metrics.go (instantQuery), filtered by $instance.
METRICS = [
    ("cpu", "CPU busy", "percent",
     '100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle",instance=~"$instance"}[1m])) * 100)', 80),
    ("memory", "Memory used", "percent",
     '100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes{instance=~"$instance"} / node_memory_MemTotal_bytes{instance=~"$instance"}))', 90),
    ("disk", "Disk used", "percent",
     '100 * (1 - avg by (instance) (node_filesystem_avail_bytes{mountpoint=~"/|/etc/hosts",instance=~"$instance"} / node_filesystem_size_bytes{mountpoint=~"/|/etc/hosts",instance=~"$instance"}))', 90),
    ("network_rx", "Network received", "Bps",
     'sum by (instance) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*",instance=~"$instance"}[1m]))', None),
    ("network_tx", "Network sent", "Bps",
     'sum by (instance) (rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*",instance=~"$instance"}[1m]))', None),
    ("load1", "Load (1m)", "short", 'avg by (instance) (node_load1{instance=~"$instance"})', None),
]

panels = []
next_id = 1


def add(panel, x, y, w, h):
    global next_id
    panel.update({"id": next_id, "gridPos": {"x": x, "y": y, "w": w, "h": h}, "datasource": DS})
    next_id += 1
    panels.append(panel)


def row(title, y):
    global next_id
    panels.append({"type": "row", "title": title, "id": next_id, "collapsed": False,
                   "gridPos": {"x": 0, "y": y, "w": 24, "h": 1}, "panels": []})
    next_id += 1


def target(expr, legend, ref):
    return {"datasource": DS, "expr": expr, "legendFormat": legend, "refId": ref}


def stat(title, expr, unit="short", mappings=None, thresholds=None, description=""):
    return {
        "type": "stat", "title": title, "description": description,
        "targets": [target(expr, "{{job}}{{instance}}", "A")],
        "fieldConfig": {"defaults": {
            "unit": unit,
            "mappings": mappings or [],
            "thresholds": thresholds or {"mode": "absolute", "steps": [{"color": "green", "value": None}]},
        }, "overrides": []},
        "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                    "colorMode": "background", "graphMode": "none", "textMode": "auto"},
    }


def timeseries(title, targets, unit, description="", threshold=None, overrides=None):
    defaults = {"unit": unit, "custom": {"lineWidth": 2, "fillOpacity": 0, "spanNulls": True}}
    if threshold is not None:
        defaults["thresholds"] = {"mode": "absolute", "steps": [
            {"color": "transparent", "value": None}, {"color": "red", "value": threshold}]}
        defaults["custom"]["thresholdsStyle"] = {"mode": "line+area"}
    return {
        "type": "timeseries", "title": title, "description": description,
        "targets": targets,
        "fieldConfig": {"defaults": defaults, "overrides": overrides or []},
        "options": {"legend": {"displayMode": "list", "placement": "bottom"}, "tooltip": {"mode": "multi"}},
    }


# Service health
row("Service health", 0)
add(stat("Targets up", 'up', mappings=[{"type": "value", "options": {
    "0": {"text": "down", "color": "red"}, "1": {"text": "up", "color": "green"}}}],
    description="Prometheus scrape health of node-exporter, the Go API and the AI service."), 0, 1, 6, 4)
add(stat("AI circuit breaker", "infurasaito_ai_breaker_state", mappings=[{"type": "value", "options": {
    "0": {"text": "closed", "color": "green"}, "1": {"text": "open", "color": "red"},
    "2": {"text": "half-open", "color": "orange"}}}],
    description="Open means the Go API is failing fast instead of calling the AI service."), 6, 1, 3, 4)
add(stat("Models trained (last run)", "infurasaito_models_trained"), 9, 1, 3, 4)
add(stat("Since last training", "time() - infurasaito_last_training_timestamp_seconds", unit="s",
         thresholds={"mode": "absolute", "steps": [{"color": "green", "value": None},
                                                   {"color": "orange", "value": 1800},
                                                   {"color": "red", "value": 3600}]}), 12, 1, 4, 4)
add(stat("Cached models (AI service)", "infurasaito_ai_cached_models"), 16, 1, 4, 4)
anomalous_now = stat("Anomalous now", 'sum(infurasaito_anomaly{instance=~"$instance"}) or vector(0)',
                     thresholds={"mode": "absolute", "steps": [{"color": "green", "value": None},
                                                               {"color": "red", "value": 1}]},
                     description="Metrics whose newest sample is far from the model's expectation "
                                 "(checked every ANOMALY_INTERVAL). Red markers on the graphs below show when.")
anomalous_now["targets"][0]["legendFormat"] = "anomalous"
add(anomalous_now, 20, 1, 4, 4)

# Actual vs forecast
row("Actual vs forecast (the forecast line is shifted 30 minutes so each prediction lines up with what happened)", 5)
annotations = []
for i, (name, title, unit, actual, threshold) in enumerate(METRICS):
    fc = f'infurasaito_forecast{{metric="{name}",instance=~"$instance",bound="%s"}} offset 30m'
    overrides = [
        {"matcher": {"id": "byRegexp", "options": ".*forecast.*"},
         "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 10]}}]},
        {"matcher": {"id": "byRegexp", "options": ".*(lower|upper).*"},
         "properties": [{"id": "custom.lineWidth", "value": 1}, {"id": "color", "value": {"mode": "fixed", "fixedColor": "#8ab8ff"}}]},
        {"matcher": {"id": "byRegexp", "options": ".*upper.*"},
         "properties": [{"id": "custom.fillBelowTo", "value": "lower bound"}, {"id": "custom.fillOpacity", "value": 15}]},
    ]
    add(timeseries(
        title,
        [target(actual, "actual {{instance}}", "A"),
         target(fc % "value", "forecast {{instance}}", "B"),
         target(fc % "lower", "lower bound", "C"),
         target(fc % "upper", "upper bound", "D")],
        unit,
        description=f"Actual `{name}` against the forecast made 30 minutes earlier, with its uncertainty band."
                    + (f" The red line is an example alert threshold ({threshold})." if threshold else ""),
        threshold=threshold, overrides=overrides,
    ), (i % 2) * 12, 6 + (i // 2) * 8, 12, 8)
    panels[-1]["description"] += " Red markers are anomalies: samples far from the model's expectation."
    expr = f'infurasaito_anomaly{{metric="{name}",instance=~"$instance"}} == 1'
    annotations.append({
        "name": f"{title} anomalies", "datasource": DS, "enable": True, "iconColor": "red",
        "expr": expr, "step": "1m", "titleFormat": f"{title} anomaly", "textFormat": "{{instance}}",
        "tagKeys": "metric,instance", "useValueForTime": False,
        "target": {"expr": expr, "refId": "Anno"},
        "filter": {"exclude": False, "ids": [panels[-1]["id"]]},
    })

# Latency and traffic
y = 6 + 3 * 8
row("API traffic and latency", y)
add(timeseries("Requests per second by route and code",
               [target('sum by (route, code) (rate(infurasaito_http_requests_total[5m]))', "{{route}} {{code}}", "A"),
                target('sum by (reason) (rate(infurasaito_http_rejected_total[5m]))', "rejected: {{reason}}", "B")],
               "reqps"), 0, y + 1, 12, 8)
add(timeseries("API latency p95 by route",
               [target('histogram_quantile(0.95, sum by (le, route) (rate(infurasaito_http_request_duration_seconds_bucket[5m])))',
                       "{{route}}", "A")], "s"), 12, y + 1, 12, 8)
add(timeseries("AI service call latency p95 by path and outcome",
               [target('histogram_quantile(0.95, sum by (le, path, outcome) (rate(infurasaito_ai_request_duration_seconds_bucket[5m])))',
                       "{{path}} {{outcome}}", "A")], "s"), 0, y + 9, 12, 8)
add(timeseries("Prophet training time p95 (AI service)",
               [target('histogram_quantile(0.95, sum by (le, endpoint) (rate(infurasaito_ai_training_seconds_bucket[15m])))',
                       "{{endpoint}}", "A")], "s"), 12, y + 9, 12, 8)

dashboard = {
    "uid": "infurasaito",
    "title": "InfuraSaito",
    "tags": ["infurasaito"],
    "timezone": "browser",
    "schemaVersion": 39,
    "version": 1,
    "refresh": "30s",
    "time": {"from": "now-6h", "to": "now"},
    "templating": {"list": [{
        "name": "instance", "label": "Instance", "type": "query", "datasource": DS,
        "query": {"query": 'label_values(up{job="node"}, instance)', "refId": "instance"},
        "definition": 'label_values(up{job="node"}, instance)',
        "includeAll": True, "multi": True, "allValue": ".*", "refresh": 2,
        "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
    }]},
    "annotations": {"list": annotations},
    "panels": panels,
}

with open(sys.argv[1] if len(sys.argv) > 1 else OUTPUT, "w", encoding="utf-8", newline="\n") as f:
    json.dump(dashboard, f, indent=2)
    f.write("\n")
print(f"{len(panels)} panels")
