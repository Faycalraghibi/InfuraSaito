package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// forecastGaugeHorizon is how far ahead the exported forecast gauges look. Graph
// `infurasaito_forecast offset 30m` against the actual metric to compare them.
const forecastGaugeHorizon = 30 * time.Minute

var (
	metricsRegistry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "infurasaito_http_requests_total",
		Help: "API requests by route and status code.",
	}, []string{"route", "code"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "infurasaito_http_request_duration_seconds",
		Help:    "API request latency by route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})

	httpRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "infurasaito_http_rejected_total",
		Help: "API requests rejected before reaching a route, by reason (unauthorized, rate_limited).",
	}, []string{"reason"})

	aiDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "infurasaito_ai_request_duration_seconds",
		Help:    "AI service call latency by path and breaker outcome (success, failure, neutral).",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"path", "outcome"})

	breakerStateGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "infurasaito_ai_breaker_state",
		Help: "AI circuit breaker state: 0 closed, 1 open, 2 half-open.",
	})

	lastTraining = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "infurasaito_last_training_timestamp_seconds",
		Help: "Unix time the last background training run finished.",
	})

	modelsTrained = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "infurasaito_models_trained",
		Help: "Models trained by the last background training run.",
	})

	forecastGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "infurasaito_forecast",
		Help: "Cached forecast for 30 minutes ahead, by metric, instance and bound (value, lower, upper).",
	}, []string{"metric", "instance", "bound"})
)

func init() {
	metricsRegistry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests, httpDuration, httpRejected, aiDuration,
		breakerStateGauge, lastTraining, modelsTrained, forecastGauge,
	)
}

// metricsHandler serves the registry in the Prometheus exposition format.
func metricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
}

// instrument counts requests and observes latency for a route. Labelling by the
// registered route (not the raw path) keeps label cardinality bounded.
func instrument(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		httpRequests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (o callOutcome) String() string {
	return [...]string{"success", "failure", "neutral"}[o]
}

// predictionAt returns the prediction closest to at, if one is within tolerance.
func predictionAt(preds []forecastPoint, at time.Time, tolerance time.Duration) (forecastPoint, bool) {
	var best forecastPoint
	bestGap := time.Duration(-1)
	for _, p := range preds {
		t, err := time.Parse(time.RFC3339, p.Time)
		if err != nil {
			continue
		}
		if gap := absDuration(t.Sub(at)); gap <= tolerance && (bestGap < 0 || gap < bestGap) {
			best, bestGap = p, gap
		}
	}
	return best, bestGap >= 0
}

// exportForecastGauge publishes the prediction forecastGaugeHorizon ahead for a metric and instance.
func exportForecastGauge(metric, instance string, preds []forecastPoint) {
	p, ok := predictionAt(preds, time.Now().Add(forecastGaugeHorizon), matchTolerance)
	if !ok {
		return
	}
	forecastGauge.WithLabelValues(metric, instance, "value").Set(p.Value)
	forecastGauge.WithLabelValues(metric, instance, "lower").Set(p.Lower)
	forecastGauge.WithLabelValues(metric, instance, "upper").Set(p.Upper)
}
