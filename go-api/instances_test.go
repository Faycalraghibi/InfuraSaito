package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSelectInstance(t *testing.T) {
	two := []promSample{{"a:9100", 1}, {"b:9100", 2}}
	tests := []struct {
		name      string
		items     []promSample
		want      string
		wantInst  string
		wantFound bool
		wantCode  int // 0 means no error
	}{
		{"no data", nil, "", "", false, 0},
		{"no data, instance given", nil, "a:9100", "", false, 0},
		{"single, none given", []promSample{{"a:9100", 1}}, "", "a:9100", true, 0},
		{"several, none given", two, "", "", false, http.StatusBadRequest},
		{"several, match", two, "b:9100", "b:9100", true, 0},
		{"several, unknown", two, "zzz:9100", "", false, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found, ierr := selectInstance(tc.items, sampleInstance, tc.want)

			code := 0
			if ierr != nil {
				code = ierr.status
			}
			if code != tc.wantCode || found != tc.wantFound || got.Instance != tc.wantInst {
				t.Errorf("got (%q, %v, %d), want (%q, %v, %d)", got.Instance, found, code, tc.wantInst, tc.wantFound, tc.wantCode)
			}
		})
	}
}

func TestSelectInstanceErrorListsInstancesSorted(t *testing.T) {
	_, _, ierr := selectInstance([]promSample{{"b:9100", 1}, {"a:9100", 2}}, sampleInstance, "")

	if ierr == nil || !strings.Contains(ierr.msg, "a:9100, b:9100") {
		t.Errorf("error = %+v, want it to list a:9100, b:9100", ierr)
	}
}

func TestInstancesHandlerListsTargets(t *testing.T) {
	calls := fakePrometheus(t, "", promVector(promSampleJSON("b:9100", 0), promSampleJSON("a:9100", 1)))

	rec := getInstances()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	if len(calls.snapshot()) != 1 || calls.snapshot()[0] != nodeUpQuery {
		t.Errorf("queries = %q, want the up query", calls.snapshot())
	}
	var body struct {
		Instances []instanceStatus `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := []instanceStatus{{"a:9100", true}, {"b:9100", false}}
	if len(body.Instances) != 2 || body.Instances[0] != want[0] || body.Instances[1] != want[1] {
		t.Errorf("instances = %+v, want %+v", body.Instances, want)
	}
}

func TestInstancesHandlerEmptyListIsArray(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)

	rec := getInstances()

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"instances":[]`) {
		t.Errorf("got %d %s, want 200 with an empty instances array", rec.Code, rec.Body)
	}
}

func TestInstancesHandlerPrometheusError(t *testing.T) {
	fakePrometheus(t, "", "")

	if rec := getInstances(); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
