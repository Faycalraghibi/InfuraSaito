package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

const (
	// recordHorizonMinutes is how far ahead each recorded forecast reaches.
	recordHorizonMinutes = 60
	// maxAccuracyDays caps the /forecast/accuracy window.
	maxAccuracyDays = 30
	// matchTolerance is how far apart a prediction and an actual sample may be to be compared;
	// half the 5-minute history step.
	matchTolerance = 150 * time.Second
)

// accuracy summarises how stored predictions compared with what actually happened.
type accuracy struct {
	// Compared is the number of predictions that had an actual value close to their target time.
	Compared int `json:"compared"`
	// MAPE is the mean absolute percentage error (actual values of zero are left out).
	MAPE *float64 `json:"mape_percent"`
	// Coverage is the share of actual values inside the predicted [lower, upper] band.
	Coverage *float64 `json:"band_coverage_percent"`
}

type accuracyResponse struct {
	MetricName string `json:"metric_name"`
	Instance   string `json:"instance,omitempty"`
	Days       int    `json:"days"`
	accuracy
}

// compareForecasts matches each prediction with the nearest actual sample within tolerance
// and computes MAPE and band coverage over the matched pairs.
func compareForecasts(preds []storedPrediction, actuals []DataPoint, tolerance time.Duration) accuracy {
	type sample struct {
		at time.Time
		y  float64
	}
	samples := make([]sample, 0, len(actuals))
	for _, a := range actuals {
		if t, err := time.Parse(time.RFC3339, a.Ds); err == nil {
			samples = append(samples, sample{t, a.Y})
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })

	var compared, inside, mapeCount int
	var mapeSum float64
	for _, p := range preds {
		// Nearest sample: the first at or after the target, or the one before it.
		i := sort.Search(len(samples), func(i int) bool { return !samples[i].at.Before(p.Target) })
		best := -1
		for _, j := range []int{i - 1, i} {
			if j < 0 || j >= len(samples) {
				continue
			}
			if best == -1 || absDuration(samples[j].at.Sub(p.Target)) < absDuration(samples[best].at.Sub(p.Target)) {
				best = j
			}
		}
		if best == -1 || absDuration(samples[best].at.Sub(p.Target)) > tolerance {
			continue
		}

		y := samples[best].y
		compared++
		if y >= p.Lower && y <= p.Upper {
			inside++
		}
		if y != 0 {
			mapeSum += math.Abs(y-p.Value) / math.Abs(y)
			mapeCount++
		}
	}

	result := accuracy{Compared: compared}
	if mapeCount > 0 {
		mape := 100 * mapeSum / float64(mapeCount)
		result.MAPE = &mape
	}
	if compared > 0 {
		coverage := 100 * float64(inside) / float64(compared)
		result.Coverage = &coverage
	}
	return result
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// toStoredPredictions converts forecast points, skipping any whose time is not RFC 3339.
func toStoredPredictions(points []forecastPoint) []storedPrediction {
	out := make([]storedPrediction, 0, len(points))
	for _, p := range points {
		t, err := time.Parse(time.RFC3339, p.Time)
		if err != nil {
			continue
		}
		out = append(out, storedPrediction{Target: t, Value: p.Value, Lower: p.Lower, Upper: p.Upper})
	}
	return out
}

// recordForecasts fetches the cached forecast for every metric and healthy instance,
// exports its 30-minutes-ahead prediction as the infurasaito_forecast gauges and, when
// storage is configured, stores the next recordHorizonMinutes so accuracy can be measured
// once the actual values arrive. It runs after each background training run; with an
// open circuit breaker it stops early.
func recordForecasts(ctx context.Context) {
	ups, err := queryPrometheusInstant(nodeUpQuery)
	if err != nil {
		log.Printf("recording forecasts: listing instances failed: %v", err)
		return
	}

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	// Drop gauges for instances that are gone or no longer have a model.
	forecastGauge.Reset()

	stored := 0
	for _, name := range names {
		for _, up := range ups {
			if up.Value != 1 {
				continue
			}
			body, err := forecastFromCache(name, up.Instance, recordHorizonMinutes)
			if err != nil {
				var openErr *circuitOpenError
				switch {
				case errors.As(err, &openErr):
					log.Printf("recording forecasts stopped: %v", err)
					return
				case !errors.Is(err, errNoCachedModel):
					log.Printf("recording forecasts: %s failed: %v", target(name, up.Instance), err)
				}
				continue
			}
			var forecast forecastBody
			if err := json.Unmarshal(body, &forecast); err != nil {
				log.Printf("recording forecasts: unreadable forecast for %s: %v", target(name, up.Instance), err)
				continue
			}
			exportForecastGauge(name, up.Instance, forecast.Predictions)

			preds := toStoredPredictions(forecast.Predictions)
			if forecasts == nil || len(preds) == 0 {
				continue
			}
			if err := forecasts.save(ctx, name, up.Instance, preds); err != nil {
				log.Printf("recording forecasts: saving %s failed: %v", target(name, up.Instance), err)
				continue
			}
			stored++
		}
	}
	if forecasts != nil {
		log.Printf("recorded %d forecasts", stored)
	}
}

// accuracyHandler compares the forecasts stored over the last ?days= (default 7, max 30)
// with the actual values from Prometheus.
func accuracyHandler(w http.ResponseWriter, r *http.Request) {
	if forecasts == nil {
		http.Error(w, "Forecast storage is not configured (set DATABASE_URL)", http.StatusServiceUnavailable)
		return
	}
	metric, def, ok := resolveMetric(w, r)
	if !ok {
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 || parsed > maxAccuracyDays {
			http.Error(w, fmt.Sprintf("days must be between 1 and %d", maxAccuracyDays), http.StatusBadRequest)
			return
		}
		days = parsed
	}
	instance := r.URL.Query().Get("instance")

	end := time.Now()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	seriesList, err := queryPrometheusRange(def.rangeQuery, start, end, historyStep)
	if err != nil {
		http.Error(w, "Failed to fetch actual values: "+err.Error(), http.StatusInternalServerError)
		return
	}
	series, _, ierr := selectInstance(seriesList, seriesInstance, instance)
	if ierr != nil {
		http.Error(w, ierr.msg, ierr.status)
		return
	}
	if series.Instance != "" {
		instance = series.Instance
	}

	preds, err := forecasts.load(r.Context(), metric, instance, start, end)
	if err != nil {
		http.Error(w, "Failed to load stored forecasts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(accuracyResponse{
		MetricName: metric,
		Instance:   instance,
		Days:       days,
		accuracy:   compareForecasts(preds, series.Points, matchTolerance),
	})
}
