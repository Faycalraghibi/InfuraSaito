package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
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

var webhookClient = &http.Client{Timeout: 10 * time.Second}

// parseAlertRules parses ALERT_RULES: a JSON array of rules. horizon_minutes defaults to 60.
func parseAlertRules(s string) ([]alertRule, error) {
	var rules []alertRule
	if err := json.Unmarshal([]byte(s), &rules); err != nil {
		return nil, fmt.Errorf("ALERT_RULES is not a JSON array of rules: %w", err)
	}
	for i := range rules {
		r := &rules[i]
		if _, ok := metrics[r.Metric]; !ok {
			return nil, fmt.Errorf("ALERT_RULES[%d]: unsupported metric %q; supported: %s", i, r.Metric, supportedMetrics())
		}
		if r.HorizonMinutes == 0 {
			r.HorizonMinutes = 60
		}
		if r.HorizonMinutes < 1 || r.HorizonMinutes > maxHorizonMinutes {
			return nil, fmt.Errorf("ALERT_RULES[%d]: horizon_minutes must be between 1 and %d", i, maxHorizonMinutes)
		}
	}
	return rules, nil
}

// alerter evaluates rules against cached forecasts and notifies a webhook. An alert is sent
// once when a crossing is first predicted and once when the forecast clears; while it stays
// predicted, repeats are suppressed.
type alerter struct {
	rules      []alertRule
	webhookURL string

	mu     sync.Mutex
	firing map[string]bool // keyed by rule index and instance
}

func newAlerter(rules []alertRule, webhookURL string) *alerter {
	return &alerter{rules: rules, webhookURL: webhookURL, firing: map[string]bool{}}
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
	url := os.Getenv("ALERT_WEBHOOK_URL")
	if url == "" {
		return nil, errors.New("ALERT_RULES is set but ALERT_WEBHOOK_URL is empty")
	}
	return newAlerter(rules, url), nil
}

// evaluate checks every rule for every healthy instance, using the cached forecasts.
// Instances without a cached model yet are skipped until a later run.
func (a *alerter) evaluate() {
	ups, err := queryPrometheusInstant(nodeUpQuery)
	if err != nil {
		log.Printf("alerts: listing instances failed: %v", err)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for i, rule := range a.rules {
		for _, up := range ups {
			if up.Value != 1 {
				continue
			}
			body, err := forecastFromCache(rule.Metric, up.Instance, rule.HorizonMinutes)
			if err != nil {
				if !errors.Is(err, errNoCachedModel) {
					log.Printf("alerts: forecast for %s/%s failed: %v", rule.Metric, up.Instance, err)
				}
				continue
			}
			var forecast forecastBody
			if err := json.Unmarshal(body, &forecast); err != nil {
				log.Printf("alerts: unreadable forecast for %s/%s: %v", rule.Metric, up.Instance, err)
				continue
			}

			key := fmt.Sprintf("%d|%s", i, up.Instance)
			c := firstCrossing(forecast.Predictions, rule.Threshold)
			switch {
			case c.WillCross && !a.firing[key]:
				if a.notify(firingPayload(rule, up.Instance, c)) {
					a.firing[key] = true
				}
			case !c.WillCross && a.firing[key]:
				if a.notify(resolvedPayload(rule, up.Instance)) {
					delete(a.firing, key)
				}
			}
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
		log.Printf("alerts: webhook failed: %v", err)
		return false
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("alerts: webhook returned status %d", resp.StatusCode)
		return false
	}
	log.Printf("alerts: sent %s alert for %s", p.Status, target(p.Metric, p.Instance))
	return true
}

func firingPayload(rule alertRule, instance string, c crossing) alertPayload {
	text := fmt.Sprintf("InfuraSaito: %s is forecast to reach %g at %s", target(rule.Metric, instance), rule.Threshold, *c.ExpectedAt)
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

func resolvedPayload(rule alertRule, instance string) alertPayload {
	return alertPayload{
		Text: fmt.Sprintf("InfuraSaito: %s is no longer forecast to reach %g within %d minutes",
			target(rule.Metric, instance), rule.Threshold, rule.HorizonMinutes),
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
