package main

import (
	"net/http"
	"sort"
	"strings"
)

// metricDef holds the PromQL for one supported metric.
type metricDef struct {
	rangeQuery   string // history for forecasting, evaluated with query_range at a 5m step
	instantQuery string // current value
}

// metrics maps the supported `metric` query values to their PromQL.
var metrics = map[string]metricDef{
	"cpu": {
		rangeQuery:   `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`,
		instantQuery: `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[1m])) * 100)`,
	},
	"memory": {
		rangeQuery:   `100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`,
		instantQuery: `100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`,
	},
}

func supportedMetrics() string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveMetric reads the `metric` query parameter (default "cpu"). For an unsupported
// value it writes a 400 response and returns ok=false.
func resolveMetric(w http.ResponseWriter, r *http.Request) (name string, def metricDef, ok bool) {
	name = r.URL.Query().Get("metric")
	if name == "" {
		name = "cpu"
	}
	def, ok = metrics[name]
	if !ok {
		http.Error(w, "Unsupported metric; supported: "+supportedMetrics(), http.StatusBadRequest)
	}
	return name, def, ok
}
