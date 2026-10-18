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
	// Percent of the root filesystem in use. Inside the node-exporter container "/" is not
	// exposed, so the bind-mounted /etc/hosts stands in for it: that reports Docker's data
	// disk (the VM disk on Docker Desktop) rather than the host's "/".
	"disk": {
		rangeQuery:   `100 * (1 - avg by (instance) (node_filesystem_avail_bytes{mountpoint=~"/|/etc/hosts"} / node_filesystem_size_bytes{mountpoint=~"/|/etc/hosts"}))`,
		instantQuery: `100 * (1 - avg by (instance) (node_filesystem_avail_bytes{mountpoint=~"/|/etc/hosts"} / node_filesystem_size_bytes{mountpoint=~"/|/etc/hosts"}))`,
	},
	// Bytes per second received on the interfaces node-exporter can see, excluding loopback
	// and container virtual interfaces (which would double-count traffic on a real host).
	// Under docker compose that is only the node-exporter container's own interface.
	"network_rx": {
		rangeQuery:   `sum by (instance) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[5m]))`,
		instantQuery: `sum by (instance) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[1m]))`,
	},
	// Bytes per second transmitted; same interface selection as network_rx.
	"network_tx": {
		rangeQuery:   `sum by (instance) (rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[5m]))`,
		instantQuery: `sum by (instance) (rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[1m]))`,
	},
	// 1-minute load average.
	"load1": {
		rangeQuery:   `avg by (instance) (node_load1)`,
		instantQuery: `avg by (instance) (node_load1)`,
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
