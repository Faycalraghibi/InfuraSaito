package main

import "testing"

func TestEveryMetricHasBothQueries(t *testing.T) {
	for name, def := range metrics {
		if def.rangeQuery == "" || def.instantQuery == "" {
			t.Errorf("metric %q: rangeQuery=%q instantQuery=%q, want both set", name, def.rangeQuery, def.instantQuery)
		}
	}
}

func TestSupportedMetrics(t *testing.T) {
	want := "cpu, disk, load1, memory, network_rx, network_tx"

	if got := supportedMetrics(); got != want {
		t.Errorf("supportedMetrics() = %q, want %q", got, want)
	}
}
