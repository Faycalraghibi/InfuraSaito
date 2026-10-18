package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// maxAnomalyWindowMinutes caps how much recent history /api/v1/anomalies checks.
const maxAnomalyWindowMinutes = 24 * 60

type AnomalyRequest struct {
	MetricName string      `json:"metric_name"`
	Instance   string      `json:"instance,omitempty"`
	History    []DataPoint `json:"history"`
}

// detectAnomalies asks the AI service to check history against the cached model
// (/anomalies). It returns errNoCachedModel when no model has been trained yet.
func detectAnomalies(metric, instance string, history []DataPoint) ([]byte, error) {
	body, status, err := postAI("/anomalies", AnomalyRequest{MetricName: metric, Instance: instance, History: history})
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, errNoCachedModel
	}
	return nil, &aiStatusError{status, body}
}

// anomaliesHandler flags recent points that are far from the cached model's expectation.
// window_minutes (default 60, max 1440) sets how much recent history is checked.
func anomaliesHandler(w http.ResponseWriter, r *http.Request) {
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}

	window := 60
	if v := r.URL.Query().Get("window_minutes"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > maxAnomalyWindowMinutes {
			http.Error(w, fmt.Sprintf("window_minutes must be between 1 and %d", maxAnomalyWindowMinutes), http.StatusBadRequest)
			return
		}
		window = parsed
	}
	instance := r.URL.Query().Get("instance")

	end := time.Now()
	seriesList, err := queryPrometheusRange(def.rangeQuery, end.Add(-time.Duration(window)*time.Minute), end, historyStep)
	if err != nil {
		http.Error(w, "Failed to fetch recent data: "+err.Error(), http.StatusInternalServerError)
		return
	}
	series, found, ierr := selectInstance(seriesList, seriesInstance, instance)
	if ierr != nil {
		http.Error(w, ierr.msg, ierr.status)
		return
	}
	if !found || len(series.Points) == 0 {
		http.Error(w, "No recent data for "+metric+"; retry shortly", http.StatusServiceUnavailable)
		return
	}

	body, err := detectAnomalies(metric, series.Instance, series.Points)
	var openErr *circuitOpenError
	switch {
	case errors.As(err, &openErr):
		w.Header().Set("Retry-After", retryAfterSeconds(openErr.retryAfter))
		http.Error(w, openErr.Error(), http.StatusServiceUnavailable)
		return
	case errors.Is(err, errNoCachedModel):
		http.Error(w, "No trained model yet for "+target(metric, series.Instance)+"; background training creates one", http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, "Anomaly detection failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
