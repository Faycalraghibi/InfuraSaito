package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

type forecastPoint struct {
	Time  string  `json:"time"`
	Value float64 `json:"value"`
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// fallbackResponse mirrors the AI service's PredictResponse schema, plus a confidence note.
type fallbackResponse struct {
	MetricName     string          `json:"metric_name"`
	Instance       string          `json:"instance,omitempty"`
	HorizonMinutes int             `json:"horizon_minutes"`
	Confidence     string          `json:"confidence"`
	Predictions    []forecastPoint `json:"predictions"`
}

type currentResponse struct {
	Metric   string  `json:"metric"`
	Instance string  `json:"instance,omitempty"`
	Value    float64 `json:"value"`
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

	instance := r.URL.Query().Get("instance")

	end := time.Now()
	start := end.Add(-14 * 24 * time.Hour)
	step := "5m"

	seriesList, err := queryPrometheusRange(def.rangeQuery, start, end, step)
	if err != nil {
		http.Error(w, "Failed to fetch historical data: "+err.Error(), http.StatusInternalServerError)
		return
	}
	series, _, ierr := selectInstance(seriesList, seriesInstance, instance)
	if ierr != nil {
		if ierr.status != http.StatusNotFound {
			http.Error(w, ierr.msg, ierr.status)
			return
		}
		// A new target may have no range history yet; the cold-start path checks it is real.
		series = promSeries{}
	}
	if series.Instance != "" {
		instance = series.Instance
	}
	history := series.Points

	if len(history) < 2 {
		// Only treat short history as a cold start if the exporter is actually up;
		// otherwise a dead exporter or broken query would hide behind the fallback.
		ups, err := queryPrometheusInstant(nodeUpQuery)
		if err != nil {
			http.Error(w, "Metrics source unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		target, found, ierr := selectInstance(ups, sampleInstance, instance)
		if ierr != nil {
			http.Error(w, ierr.msg, ierr.status)
			return
		}
		if !found || target.Value != 1 {
			http.Error(w, "Metrics source unavailable: node-exporter "+instanceLabel(instance)+"is not being scraped", http.StatusServiceUnavailable)
			return
		}
		if target.Instance != "" {
			instance = target.Instance
		}

		// Report the current value: unlike a fixed baseline, it is meaningful for any unit.
		samples, err := queryPrometheusInstant(def.instantQuery)
		if err != nil {
			http.Error(w, "Not enough data yet for "+metric+": "+err.Error()+"; retry shortly", http.StatusServiceUnavailable)
			return
		}
		sample, found, ierr := selectInstance(samples, sampleInstance, instance)
		if ierr != nil {
			http.Error(w, ierr.msg, ierr.status)
			return
		}
		if !found {
			http.Error(w, "Not enough data yet for "+metric+"; retry shortly", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fallbackResponse{
			MetricName:     metric,
			Instance:       sample.Instance,
			HorizonMinutes: horizonMin,
			Confidence:     "none - insufficient history, showing current value",
			Predictions: []forecastPoint{
				{Time: time.Now().UTC().Format(time.RFC3339), Value: sample.Value, Lower: sample.Value, Upper: sample.Value},
			},
		})
		return
	}

	aiResponseBytes, err := forecastFromCache(metric, instance, horizonMin)
	if err != nil {
		if !errors.Is(err, errNoCachedModel) {
			log.Printf("cached forecast for %s/%s failed, training on request: %v", metric, instance, err)
		}
		aiResponseBytes, err = callForecastModel(metric, instance, history, horizonMin)
	}
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

	instance := r.URL.Query().Get("instance")

	samples, err := queryPrometheusInstant(def.instantQuery)
	if err != nil {
		http.Error(w, "Failed to fetch metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sample, found, ierr := selectInstance(samples, sampleInstance, instance)
	if ierr != nil {
		http.Error(w, ierr.msg, ierr.status)
		return
	}
	if !found {
		http.Error(w, "No data yet for "+metric+"; retry shortly", http.StatusServiceUnavailable)
		return
	}

	response := currentResponse{Metric: metric, Instance: sample.Instance, Value: sample.Value}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
