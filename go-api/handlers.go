package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

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

// maxHorizonMinutes caps forecasts at 7 days; longer horizons are not meaningful for
// 14 days of history and would make Prophet build very large frames.
const maxHorizonMinutes = 7 * 24 * 60

func forecastHandler(w http.ResponseWriter, r *http.Request) {
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}

	horizonStr := r.URL.Query().Get("horizon_minutes")
	horizonMin := 60
	if h, err := strconv.Atoi(horizonStr); err == nil && h > 0 {
		if h > maxHorizonMinutes {
			http.Error(w, fmt.Sprintf("horizon_minutes must be at most %d", maxHorizonMinutes), http.StatusBadRequest)
			return
		}
		horizonMin = h
	}

	end := time.Now()
	start := end.Add(-14 * 24 * time.Hour)
	step := "5m"

	history, err := queryPrometheusRange(def.rangeQuery, start, end, step)
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

		// Report the current value: unlike a fixed baseline, it is meaningful for any unit.
		current, err := queryPrometheus(def.instantQuery)
		if err != nil {
			http.Error(w, "Not enough data yet for "+metric+": "+err.Error()+"; retry shortly", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fallbackResponse{
			MetricName:     metric,
			HorizonMinutes: horizonMin,
			Confidence:     "none - insufficient history, showing current value",
			Predictions: []forecastPoint{
				{Time: time.Now().UTC().Format(time.RFC3339), Value: current, Lower: current, Upper: current},
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
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}

	val, err := queryPrometheus(def.instantQuery)
	if err != nil {
		http.Error(w, "Failed to fetch metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"metric": metric,
		"value":  val,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
