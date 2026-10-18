package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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
	p := hook.payloads[0]
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
	if hook.payloads[0].Instance == hook.payloads[1].Instance {
		t.Errorf("both alerts are for %q, want a:9100 and b:9100", hook.payloads[0].Instance)
	}
}
