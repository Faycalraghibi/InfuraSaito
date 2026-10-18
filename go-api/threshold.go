package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
)

// crossing describes when a forecast first reaches a threshold.
type crossing struct {
	// WillCross is true when the expected value reaches the threshold within the horizon.
	WillCross bool `json:"will_cross"`
	// ExpectedAt is the first prediction whose expected value reaches the threshold.
	ExpectedAt *string `json:"expected_at"`
	// PossibleAt is the first prediction whose upper bound reaches it (an earlier warning).
	PossibleAt *string `json:"possible_at"`
}

type thresholdResponse struct {
	MetricName     string  `json:"metric_name"`
	Instance       string  `json:"instance,omitempty"`
	Threshold      float64 `json:"threshold"`
	HorizonMinutes int     `json:"horizon_minutes"`
	crossing
}

// firstCrossing finds the first predictions whose value and upper bound reach threshold.
func firstCrossing(preds []forecastPoint, threshold float64) crossing {
	var c crossing
	for i := range preds {
		p := &preds[i]
		if c.ExpectedAt == nil && p.Value >= threshold {
			c.ExpectedAt = &p.Time
		}
		if c.PossibleAt == nil && p.Upper >= threshold {
			c.PossibleAt = &p.Time
		}
	}
	c.WillCross = c.ExpectedAt != nil
	return c
}

// thresholdHandler answers when a metric is forecast to reach ?threshold=, using the same
// forecast (and parameters) as /api/v1/forecast.
func thresholdHandler(w http.ResponseWriter, r *http.Request) {
	threshold, err := strconv.ParseFloat(r.URL.Query().Get("threshold"), 64)
	if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		http.Error(w, "threshold must be a finite number", http.StatusBadRequest)
		return
	}

	body, ok := produceForecast(w, r)
	if !ok {
		return
	}

	var forecast struct {
		MetricName     string          `json:"metric_name"`
		Instance       string          `json:"instance"`
		HorizonMinutes int             `json:"horizon_minutes"`
		Predictions    []forecastPoint `json:"predictions"`
	}
	if err := json.Unmarshal(body, &forecast); err != nil {
		http.Error(w, "AI Forecasting failed: unreadable forecast: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(thresholdResponse{
		MetricName:     forecast.MetricName,
		Instance:       forecast.Instance,
		Threshold:      threshold,
		HorizonMinutes: forecast.HorizonMinutes,
		crossing:       firstCrossing(forecast.Predictions, threshold),
	})
}
