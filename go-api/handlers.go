package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// metricQueries maps the supported `metric` values to their PromQL range queries.
var metricQueries = map[string]string{
	"cpu":    `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`,
	"memory": `100 * (1 - avg by (instance) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))`,
}

// exporterUpQuery checks that the node-exporter scrape target is healthy.
const exporterUpQuery = `max(up{job="node"})`

type forecastPoint struct {
	Time  string  `json:"time"`
	Value float64 `json:"value"`
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// fallbackResponse mirrors the AI service's PredictResponse schema, plus a confidence note.
type fallbackResponse struct {
	MetricName     string          `json:"metric_name"`
	HorizonMinutes int             `json:"horizon_minutes"`
	Confidence     string          `json:"confidence"`
	Predictions    []forecastPoint `json:"predictions"`
}

func supportedMetrics() string {
	names := make([]string, 0, len(metricQueries))
	for name := range metricQueries {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func forecastHandler(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "cpu"
	}

	query, ok := metricQueries[metric]
	if !ok {
		http.Error(w, "Unsupported metric; supported: "+supportedMetrics(), http.StatusBadRequest)
		return
	}

	horizonStr := r.URL.Query().Get("horizon_minutes")
	horizonMin := 60
	if h, err := strconv.Atoi(horizonStr); err == nil && h > 0 {
		horizonMin = h
	}

	end := time.Now()
	start := end.Add(-14 * 24 * time.Hour)
	step := "5m"

	history, err := queryPrometheusRange(query, start, end, step)
	if err != nil {
		http.Error(w, "Failed to fetch historical data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if len(history) < 2 {
		// Only treat short history as a cold start if the exporter is actually up;
		// otherwise a dead exporter or broken query would hide behind the fallback.
		up, err := queryPrometheus(exporterUpQuery)
		if err != nil || up != 1 {
			http.Error(w, "Metrics source unavailable: node-exporter is not being scraped", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fallbackResponse{
			MetricName:     metric,
			HorizonMinutes: horizonMin,
			Confidence:     "none - insufficient history",
			Predictions: []forecastPoint{
				{Time: time.Now().UTC().Format(time.RFC3339), Value: 30.0, Lower: 0.0, Upper: 100.0},
			},
		})
		return
	}

	aiResponseBytes, err := callForecastModel(metric, history, horizonMin)
	if err != nil {
		http.Error(w, "AI Forecasting failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(aiResponseBytes)
}

func currentMetricsHandler(w http.ResponseWriter, r *http.Request) {
	query := `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[1m])) * 100)`

	val, err := queryPrometheus(query)
	if err != nil {
		http.Error(w, "Failed to fetch metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"metric": "cpu",
		"value":  val,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
