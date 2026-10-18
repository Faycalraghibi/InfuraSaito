package main

import (
	"net/http"
	"sort"
	"testing"
	"time"
)

func TestTrainAllTrainsEverySeriesWithEnoughHistory(t *testing.T) {
	// Every metric's range query gets the same answer: a:9100 has 3 points, b:9100 only 1.
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2, 3), promSeriesJSON("b:9100", 4)), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})

	trained := trainAll()

	if trained != len(metrics) {
		t.Fatalf("trained = %d, want %d (one per metric for a:9100)", trained, len(metrics))
	}
	var names []string
	for _, call := range ai.calls {
		if call.path != "/train" || call.body["instance"] != "a:9100" {
			t.Errorf("call = %s %v, want /train for a:9100", call.path, call.body)
		}
		if history, _ := call.body["history"].([]any); len(history) != 3 {
			t.Errorf("history has %d points, want 3", len(history))
		}
		names = append(names, call.body["metric_name"].(string))
	}
	sort.Strings(names)
	want := make([]string, 0, len(metrics))
	for name := range metrics {
		want = append(want, name)
	}
	sort.Strings(want)
	if len(names) != len(want) {
		t.Fatalf("trained metrics %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("trained metrics %v, want %v", names, want)
			break
		}
	}
}

func TestTrainAllSkipsPrometheusAndAIFailures(t *testing.T) {
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2)), "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusInternalServerError, `{}`}})

	if trained := trainAll(); trained != 0 {
		t.Errorf("trained = %d, want 0 when /train fails", trained)
	}
	if len(ai.calls) != len(metrics) {
		t.Errorf("AI calls = %d, want one attempt per metric (%d)", len(ai.calls), len(metrics))
	}
}

func TestTrainAllWithPrometheusDown(t *testing.T) {
	fakePrometheus(t, "", "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})

	if trained := trainAll(); trained != 0 || len(ai.calls) != 0 {
		t.Errorf("trained = %d with %d AI calls, want 0 and 0", trained, len(ai.calls))
	}
}

func TestTrainIntervalFromEnv(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
	}{
		{"", 15 * time.Minute},
		{"0", 0},
		{"30s", 30 * time.Second},
		{"soon", 15 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("TRAIN_INTERVAL", tc.value)

			if got := trainIntervalFromEnv(); got != tc.want {
				t.Errorf("trainIntervalFromEnv() = %s, want %s", got, tc.want)
			}
		})
	}
}
