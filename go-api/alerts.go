package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// alertRule fires when metric is forecast to reach threshold within horizon_minutes.
type alertRule struct {
	Metric         string  `json:"metric"`
	Threshold      float64 `json:"threshold"`
	HorizonMinutes int     `json:"horizon_minutes"`
}

// alertPayload is the JSON body POSTed to the webhook. Text makes it render in Slack
// incoming webhooks; the other fields are for programmatic consumers.
type alertPayload struct {
	Text       string  `json:"text"`
	Status     string  `json:"status"` // "firing" or "resolved"
	Metric     string  `json:"metric"`
	Instance   string  `json:"instance,omitempty"`
	Threshold  float64 `json:"threshold"`
	ExpectedAt *string `json:"expected_at,omitempty"`
	PossibleAt *string `json:"possible_at,omitempty"`
}

// webhookClient treats redirects as failures: following one would turn the POST into a
// body-less GET that could "succeed" without delivering anything.
var webhookClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// parseAlertRules parses ALERT_RULES: a non-empty JSON array of rules. Every rule needs a
// supported metric and a threshold; horizon_minutes defaults to 60. Unknown fields are
// rejected so a typo cannot silently become a zero threshold.
func parseAlertRules(s string) ([]alertRule, error) {
	var raw []struct {
		Metric         string   `json:"metric"`
		Threshold      *float64 `json:"threshold"`
		HorizonMinutes int      `json:"horizon_minutes"`
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("ALERT_RULES is not a JSON array of rules: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("ALERT_RULES contains no rules")
	}

	rules := make([]alertRule, len(raw))
	for i, r := range raw {
		if _, ok := metrics[r.Metric]; !ok {
			return nil, fmt.Errorf("ALERT_RULES[%d]: unsupported metric %q; supported: %s", i, r.Metric, supportedMetrics())
		}
		if r.Threshold == nil {
			return nil, fmt.Errorf("ALERT_RULES[%d]: threshold is required", i)
		}
		horizon := r.HorizonMinutes
		if horizon == 0 {
			horizon = 60
		}
		if horizon < 1 || horizon > maxHorizonMinutes {
			return nil, fmt.Errorf("ALERT_RULES[%d]: horizon_minutes must be between 1 and %d", i, maxHorizonMinutes)
		}
		rules[i] = alertRule{Metric: r.Metric, Threshold: *r.Threshold, HorizonMinutes: horizon}
	}
	return rules, nil
}

// alertKey identifies one rule on one instance.
type alertKey struct {
	rule     int
	instance string
}

// alerter evaluates rules against cached forecasts and notifies a webhook. An alert is sent
// once when a crossing is first predicted and once when the forecast clears; while it stays
// predicted, repeats are suppressed.
type alerter struct {
	rules      []alertRule
	webhookURL string
	now        func() time.Time

	mu     sync.Mutex
	firing map[alertKey]bool
}

func newAlerter(rules []alertRule, webhookURL string) *alerter {
	return &alerter{rules: rules, webhookURL: webhookURL, now: time.Now, firing: map[alertKey]bool{}}
}

// alerterFromEnv builds the alerter from ALERT_RULES and ALERT_WEBHOOK_URL. It returns nil
// when no rules are configured, and an error for an invalid configuration.
func alerterFromEnv() (*alerter, error) {
	raw := os.Getenv("ALERT_RULES")
	if raw == "" {
		return nil, nil
	}
	rules, err := parseAlertRules(raw)
	if err != nil {
		return nil, err
	}
	webhook := os.Getenv("ALERT_WEBHOOK_URL")
	if webhook == "" {
		return nil, errors.New("ALERT_RULES is set but ALERT_WEBHOOK_URL is empty")
	}
	return newAlerter(rules, webhook), nil
}

// evaluate checks every rule for every healthy instance, using the cached forecasts.
// It runs on the trainer goroutine, serially; with an open circuit breaker it stops early.
//
// A firing alert stays firing while its instance is down or its forecast is unavailable;
// it is resolved when the forecast clears or the instance is no longer scraped at all.
func (a *alerter) evaluate() {
	ups, err := queryPrometheusInstant(nodeUpQuery)
	if err != nil {
		log.Printf("alerts: listing instances failed: %v", err)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	present := map[string]bool{}
	for _, up := range ups {
		present[up.Instance] = true
	}

	for i, rule := range a.rules {
		for _, up := range ups {
			if up.Value != 1 {
				continue
			}
			body, err := forecastFromCache(rule.Metric, up.Instance, rule.HorizonMinutes)
			if err != nil {
				var openErr *circuitOpenError
				switch {
				case errors.As(err, &openErr):
					log.Printf("alerts: evaluation stopped: %v", err)
					return
				case !errors.Is(err, errNoCachedModel):
					log.Printf("alerts: forecast for %s/%s failed: %v", rule.Metric, up.Instance, err)
				}
				continue
			}
			var forecast forecastBody
			if err := json.Unmarshal(body, &forecast); err != nil {
				log.Printf("alerts: unreadable forecast for %s/%s: %v", rule.Metric, up.Instance, err)
				continue
			}

			key := alertKey{i, up.Instance}
			c := firstCrossing(forecast.Predictions, rule.Threshold)
			switch {
			case c.WillCross && !a.firing[key]:
				if a.notify(a.firingPayload(rule, up.Instance, c)) {
					a.firing[key] = true
				}
			case !c.WillCross && a.firing[key]:
				if a.notify(resolvedPayload(rule, up.Instance, "is no longer forecast to reach")) {
					delete(a.firing, key)
				}
			}
		}
	}

	// Resolve alerts for targets Prometheus no longer scrapes, so they don't stay open forever.
	for key := range a.firing {
		if present[key.instance] {
			continue
		}
		if a.notify(resolvedPayload(a.rules[key.rule], key.instance, "is no longer monitored; stopped alerting on")) {
			delete(a.firing, key)
		}
	}
}

// notify POSTs p to the webhook and reports whether it was accepted. On failure the alert
// state is left unchanged, so the next run retries.
func (a *alerter) notify(p alertPayload) bool {
	data, err := json.Marshal(p)
	if err != nil {
		log.Printf("alerts: encoding payload failed: %v", err)
		return false
	}
	resp, err := webhookClient.Post(a.webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		// The URL is a credential (e.g. a Slack webhook token); never log it.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		log.Printf("alerts: webhook failed: %v", err)
		return false
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("alerts: webhook returned status %d", resp.StatusCode)
		return false
	}
	log.Printf("alerts: sent %s alert for %s", p.Status, target(p.Metric, p.Instance))
	return true
}

func (a *alerter) firingPayload(rule alertRule, instance string, c crossing) alertPayload {
	// Cached forecasts start at the model's last training point, so the first crossing can
	// already be in the past: the metric is at or above the threshold now.
	var text string
	if at, err := time.Parse(time.RFC3339, *c.ExpectedAt); err == nil && !at.After(a.now()) {
		text = fmt.Sprintf("InfuraSaito: %s is at or above %g (forecast %s)", target(rule.Metric, instance), rule.Threshold, *c.ExpectedAt)
	} else {
		text = fmt.Sprintf("InfuraSaito: %s is forecast to reach %g at %s", target(rule.Metric, instance), rule.Threshold, *c.ExpectedAt)
	}
	if c.PossibleAt != nil && *c.PossibleAt != *c.ExpectedAt {
		text += fmt.Sprintf(" (possibly from %s)", *c.PossibleAt)
	}
	return alertPayload{
		Text:       text,
		Status:     "firing",
		Metric:     rule.Metric,
		Instance:   instance,
		Threshold:  rule.Threshold,
		ExpectedAt: c.ExpectedAt,
		PossibleAt: c.PossibleAt,
	}
}

func resolvedPayload(rule alertRule, instance, reason string) alertPayload {
	return alertPayload{
		Text: fmt.Sprintf("InfuraSaito: %s %s %g within %d minutes",
			target(rule.Metric, instance), reason, rule.Threshold, rule.HorizonMinutes),
		Status:    "resolved",
		Metric:    rule.Metric,
		Instance:  instance,
		Threshold: rule.Threshold,
	}
}

// target names a metric on an instance for messages, e.g. "cpu on node-exporter:9100".
func target(metric, instance string) string {
	if instance == "" {
		return metric
	}
	return metric + " on " + instance
}
