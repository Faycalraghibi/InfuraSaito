package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

// maxAnomalyWindowMinutes caps how much recent history /api/v1/anomalies checks.
const maxAnomalyWindowMinutes = 24 * 60

// anomalyCheckWindow is how much recent history each background check reads. With the
// 5m step that yields a sample at the start and one at the end; only the newest is checked.
const anomalyCheckWindow = 5 * time.Minute

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

// anomalyBody is the part of the AI service's /anomalies response the checker reads.
type anomalyBody struct {
	Anomalies []json.RawMessage `json:"anomalies"`
}

// checkLatestAnomalies checks the newest sample of every metric and instance against its
// cached model and exports the result as infurasaito_anomaly (1 anomalous, 0 normal),
// counting anomalous checks in infurasaito_anomalies_total. Series without a model yet
// are skipped; with an open circuit breaker it stops early.
func checkLatestAnomalies() {
	end := time.Now()

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	// Drop gauges for instances that are gone or no longer have a model.
	anomalyGauge.Reset()

	for _, name := range names {
		seriesList, err := queryPrometheusRange(metrics[name].rangeQuery, end.Add(-anomalyCheckWindow), end, historyStep)
		if err != nil {
			log.Printf("anomaly check %s: fetching recent data failed: %v", name, err)
			continue
		}
		for _, series := range seriesList {
			if len(series.Points) == 0 {
				continue
			}
			latest := series.Points[len(series.Points)-1:]
			body, err := detectAnomalies(name, series.Instance, latest)
			if err != nil {
				var openErr *circuitOpenError
				switch {
				case errors.As(err, &openErr):
					log.Printf("anomaly check stopped: %v", err)
					return
				case !errors.Is(err, errNoCachedModel):
					log.Printf("anomaly check %s failed: %v", target(name, series.Instance), err)
				}
				continue
			}
			var result anomalyBody
			if err := json.Unmarshal(body, &result); err != nil {
				log.Printf("anomaly check: unreadable response for %s: %v", target(name, series.Instance), err)
				continue
			}
			anomalous := 0.0
			if len(result.Anomalies) > 0 {
				anomalous = 1
				anomaliesTotal.WithLabelValues(name, series.Instance).Inc()
			}
			anomalyGauge.WithLabelValues(name, series.Instance).Set(anomalous)
		}
	}
}

// startAnomalyChecker runs checkLatestAnomalies every interval until ctx is cancelled.
func startAnomalyChecker(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkLatestAnomalies()
			}
		}
	}()
}

// anomalyIntervalFromEnv reads ANOMALY_INTERVAL (a Go duration, default 1m); "0" disables
// the background anomaly check.
func anomalyIntervalFromEnv() time.Duration {
	if os.Getenv("ANOMALY_INTERVAL") == "0" {
		return 0
	}
	return durationFromEnv("ANOMALY_INTERVAL", time.Minute)
}
