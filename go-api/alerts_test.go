package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// webhookRecorder is a fake alert webhook that records payloads and answers with statuses
// from a queue (200 once the queue is empty).
type webhookRecorder struct {
	mu       sync.Mutex
	payloads []alertPayload
	statuses []int
	url      string
}

func fakeWebhook(t *testing.T, statuses ...int) *webhookRecorder {
	t.Helper()
	rec := &webhookRecorder{statuses: statuses}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var p alertPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Errorf("webhook got invalid JSON %q: %v", raw, err)
		}
		rec.mu.Lock()
		rec.payloads = append(rec.payloads, p)
		status := http.StatusOK
		if len(rec.statuses) > 0 {
			status, rec.statuses = rec.statuses[0], rec.statuses[1:]
		}
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	rec.url = srv.URL
	return rec
}

func (r *webhookRecorder) statusesSent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.payloads))
	for i, p := range r.payloads {
		out[i] = p.Status
	}
	return out
}

func (r *webhookRecorder) payload(i int) alertPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.payloads[i]
}

const (
	crossingForecast = `{"metric_name":"cpu","instance":"a:9100","horizon_minutes":60,
		"predictions":[{"time":"t1","value":50,"lower":40,"upper":85},{"time":"t2","value":90,"lower":80,"upper":99}]}`
	calmForecast = `{"metric_name":"cpu","instance":"a:9100","horizon_minutes":60,
		"predictions":[{"time":"t1","value":20,"lower":10,"upper":30}]}`
)

func TestParseAlertRules(t *testing.T) {
	rules, err := parseAlertRules(`[{"metric":"cpu","threshold":80,"horizon_minutes":30},{"metric":"disk","threshold":90}]`)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []alertRule{{"cpu", 80, 30}, {"disk", 90, 60}}
	if len(rules) != 2 || rules[0] != want[0] || rules[1] != want[1] {
		t.Errorf("rules = %+v, want %+v (horizon defaults to 60)", rules, want)
	}
}

func TestParseAlertRulesRejectsBadInput(t *testing.T) {
	for _, input := range []string{
		`not json`,
		`[{"metric":"bogus","threshold":1}]`,
		`[{"metric":"cpu","threshold":1,"horizon_minutes":10081}]`,
		`[{"metric":"cpu","threshold":1,"horizon_minutes":-5}]`,
		`[{"metric":"cpu","horizon":30}]`, // typo'd field and no threshold
		`[{"metric":"cpu"}]`,              // no threshold
		`[]`,
		`null`,
	} {
		if _, err := parseAlertRules(input); err == nil {
			t.Errorf("parseAlertRules(%s) = nil error, want an error", input)
		}
	}
}

func TestAlerterFromEnv(t *testing.T) {
	t.Run("no rules", func(t *testing.T) {
		t.Setenv("ALERT_RULES", "")
		t.Setenv("ALERT_WEBHOOK_URL", "http://example.invalid")
		if a, err := alerterFromEnv(); a != nil || err != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", a, err)
		}
	})
	t.Run("rules without webhook", func(t *testing.T) {
		t.Setenv("ALERT_RULES", `[{"metric":"cpu","threshold":80}]`)
		t.Setenv("ALERT_WEBHOOK_URL", "")
		if _, err := alerterFromEnv(); err == nil {
			t.Error("want an error when rules are set without a webhook URL")
		}
	})
	t.Run("invalid rules", func(t *testing.T) {
		t.Setenv("ALERT_RULES", `[{"metric":"bogus","threshold":80}]`)
		t.Setenv("ALERT_WEBHOOK_URL", "http://example.invalid")
		if _, err := alerterFromEnv(); err == nil {
			t.Error("want an error for invalid rules")
		}
	})
	t.Run("valid", func(t *testing.T) {
		t.Setenv("ALERT_RULES", `[{"metric":"cpu","threshold":80}]`)
		t.Setenv("ALERT_WEBHOOK_URL", "http://example.invalid")
		a, err := alerterFromEnv()
		if err != nil || a == nil || len(a.rules) != 1 || a.webhookURL != "http://example.invalid" {
			t.Errorf("got (%+v, %v), want an alerter with 1 rule", a, err)
		}
	})
}

func TestAlerterFiresOnceAndResolves(t *testing.T) {
	hook := fakeWebhook(t)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)

	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a.evaluate()
	a.evaluate() // still crossing: suppressed

	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, calmForecast}})
	a.evaluate() // cleared: resolved
	a.evaluate() // still clear: nothing

	got := hook.statusesSent()
	if len(got) != 2 || got[0] != "firing" || got[1] != "resolved" {
		t.Fatalf("webhook statuses = %v, want [firing resolved]", got)
	}
	p := hook.payload(0)
	if p.Metric != "cpu" || p.Instance != "a:9100" || p.Threshold != 80 || deref(p.ExpectedAt) != "t2" || deref(p.PossibleAt) != "t1" || p.Text == "" {
		t.Errorf("firing payload = %+v, want cpu/a:9100/80 expected t2, possible t1, with text", p)
	}
}

func TestAlerterRetriesAfterWebhookFailure(t *testing.T) {
	hook := fakeWebhook(t, http.StatusInternalServerError)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)

	a.evaluate() // webhook fails: not marked as firing
	a.evaluate() // retried and delivered
	a.evaluate() // suppressed

	if got := hook.statusesSent(); len(got) != 2 {
		t.Errorf("webhook calls = %v, want 2 (one failed, one retried)", got)
	}
}

func TestAlerterSkipsInstancesWithoutForecasts(t *testing.T) {
	hook := fakeWebhook(t)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1), promSampleJSON("down:9100", 0)))
	ai := fakeAIRoutes(t, map[string]aiReply{}) // every /forecast answers 404: no cached model
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)

	a.evaluate()

	if n := len(hook.statusesSent()); n != 0 {
		t.Errorf("webhook calls = %d, want 0", n)
	}
	if n := ai.count(); n != 1 {
		t.Errorf("AI calls = %d, want 1 (the down instance is skipped)", n)
	}
}

func TestAlerterTracksInstancesSeparately(t *testing.T) {
	hook := fakeWebhook(t)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1), promSampleJSON("b:9100", 1)))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)

	a.evaluate()

	got := hook.statusesSent()
	if len(got) != 2 {
		t.Fatalf("webhook calls = %v, want one firing alert per instance", got)
	}
	if hook.payload(0).Instance == hook.payload(1).Instance {
		t.Errorf("both alerts are for %q, want a:9100 and b:9100", hook.payload(0).Instance)
	}
}

func TestAlerterKeepsFiringWhileForecastIsUnavailable(t *testing.T) {
	hook := fakeWebhook(t)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a.evaluate()

	fakeAIRoutes(t, map[string]aiReply{}) // no cached model
	a.evaluate()
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusInternalServerError, `{}`}})
	a.evaluate()

	if got := hook.statusesSent(); len(got) != 1 || got[0] != "firing" {
		t.Errorf("webhook statuses = %v, want only [firing]: an unavailable forecast is not a resolution", got)
	}
}

func TestAlerterRetriesFailedResolve(t *testing.T) {
	hook := fakeWebhook(t, http.StatusOK, http.StatusInternalServerError)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a.evaluate()

	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, calmForecast}})
	a.evaluate() // resolve fails
	a.evaluate() // retried
	a.evaluate() // done

	if got := hook.statusesSent(); len(got) != 3 || got[1] != "resolved" || got[2] != "resolved" {
		t.Errorf("webhook statuses = %v, want [firing resolved resolved]", got)
	}
}

func TestAlerterResolvesTargetsThatAreNoLongerScraped(t *testing.T) {
	hook := fakeWebhook(t)
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	a.evaluate()

	fakePrometheus(t, "", promVector(promSampleJSON("b:9100", 0))) // a:9100 is gone
	a.evaluate()

	got := hook.statusesSent()
	if len(got) != 2 || got[1] != "resolved" || hook.payload(1).Instance != "a:9100" {
		t.Fatalf("webhook statuses = %v, want [firing resolved] for a:9100", got)
	}
	if !strings.Contains(hook.payload(1).Text, "no longer monitored") {
		t.Errorf("resolved text = %q, want it to say the target is no longer monitored", hook.payload(1).Text)
	}
}

func TestAlerterKeepsFiringWhileInstanceIsDown(t *testing.T) {
	hook := fakeWebhook(t)
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, hook.url)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	a.evaluate()

	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 0)))
	a.evaluate()

	if got := hook.statusesSent(); len(got) != 1 {
		t.Errorf("webhook statuses = %v, want only [firing] while the target is down", got)
	}
}

func TestAlerterStopsWhenTheBreakerIsOpen(t *testing.T) {
	b := newCircuitBreaker(1, time.Minute)
	call(t, b, outcomeFailure)
	useBreaker(t, b)
	hook := fakeWebhook(t)
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1), promSampleJSON("b:9100", 1)))
	ai := fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}, {"memory", 90, 60}}, hook.url)

	a.evaluate()

	if ai.count() != 0 || len(hook.statusesSent()) != 0 {
		t.Errorf("AI calls = %d, webhook calls = %d; want 0 and 0", ai.count(), len(hook.statusesSent()))
	}
}

func TestAlerterNeverLogsTheWebhookURL(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1)))
	fakeAIRoutes(t, map[string]aiReply{"/forecast": {http.StatusOK, crossingForecast}})
	a := newAlerter([]alertRule{{"cpu", 80, 60}}, "http://127.0.0.1:1/services/T000/B000/SECRETTOKEN")

	a.evaluate() // the webhook is unreachable

	if strings.Contains(logs.String(), "SECRETTOKEN") {
		t.Errorf("logs contain the webhook URL:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "webhook failed") {
		t.Errorf("logs = %q, want a webhook failure line", logs.String())
	}
}

func TestAlerterWordsPastCrossingsAsCurrent(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	a := newAlerter(nil, "")
	a.now = func() time.Time { return now }
	past, future := "2026-10-18T11:55:00+00:00", "2026-10-18T12:30:00+00:00"

	if text := a.firingPayload(alertRule{"cpu", 80, 60}, "a:9100", crossing{true, &past, &past}).Text; !strings.Contains(text, "is at or above 80") {
		t.Errorf("past crossing text = %q, want 'is at or above 80'", text)
	}
	if text := a.firingPayload(alertRule{"cpu", 80, 60}, "a:9100", crossing{true, &future, &future}).Text; !strings.Contains(text, "is forecast to reach 80 at") {
		t.Errorf("future crossing text = %q, want 'is forecast to reach 80 at'", text)
	}
}
