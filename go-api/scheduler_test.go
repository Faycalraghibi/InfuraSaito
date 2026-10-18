package main

import (
	"context"
	"net/http"
	"sort"
	"sync/atomic"
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
	for _, call := range ai.snapshot() {
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
	if ai.count() != len(metrics) {
		t.Errorf("AI calls = %d, want one attempt per metric (%d)", ai.count(), len(metrics))
	}
}

func TestTrainAllWithPrometheusDown(t *testing.T) {
	fakePrometheus(t, "", "")
	ai := fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})

	if trained := trainAll(); trained != 0 || ai.count() != 0 {
		t.Errorf("trained = %d with %d AI calls, want 0 and 0", trained, ai.count())
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

func TestStartTrainerRetriesUntilFirstSuccess(t *testing.T) {
	prom := fakePrometheus(t, promRange(), "")
	fakeAIRoutes(t, map[string]aiReply{"/train": {http.StatusOK, `{}`}})
	old := firstTrainingRetry
	firstTrainingRetry = 20 * time.Millisecond
	t.Cleanup(func() { firstTrainingRetry = old })
	ctx, cancel := context.WithCancel(context.Background())
	// Registered last, so it runs first: stop the trainer before the fakes are torn down.
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
	})

	var afterRuns atomic.Int32
	startTrainer(ctx, time.Hour, func() { afterRuns.Add(1) })
	time.Sleep(150 * time.Millisecond)
	cancel()

	if n := prom.count(); n <= len(metrics) {
		t.Errorf("Prometheus range queries = %d, want more than %d (a retry after the empty first run)", n, len(metrics))
	}
	if n := afterRuns.Load(); n < 2 {
		t.Errorf("afterRun called %d times, want once per training run (at least 2)", n)
	}
}
