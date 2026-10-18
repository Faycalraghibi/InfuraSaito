package main

import (
	"testing"
	"time"
)

func TestQueryPrometheusRangeParsesValues(t *testing.T) {
	fakePrometheus(t, promRange(1.5, 2.5), "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d points, want 2", len(pts))
	}
	wantDs := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	if pts[0].Ds != wantDs || pts[0].Y != 1.5 || pts[1].Y != 2.5 {
		t.Errorf("got %+v, want first point {%s 1.5} and second y=2.5", pts, wantDs)
	}
}

func TestQueryPrometheusRangeSkipsMalformedRows(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` +
		`[1700000000,"1"],[1700000300,"not-a-number"],["bad-ts","2"],[1700000600,"3"]]}]}}`
	fakePrometheus(t, body, "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pts) != 2 || pts[0].Y != 1 || pts[1].Y != 3 {
		t.Errorf("got %+v, want only the two well-formed rows", pts)
	}
}

func TestQueryPrometheusRangeEmptyResultIsNotAnError(t *testing.T) {
	fakePrometheus(t, promRange(), "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil || len(pts) != 0 {
		t.Errorf("got (%v, %v), want (empty, nil)", pts, err)
	}
}

func TestQueryPrometheusRangeErrorStatus(t *testing.T) {
	fakePrometheus(t, `{"status":"error","data":{"resultType":"","result":[]}}`, "")

	if _, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m"); err == nil {
		t.Error("want error for status=error, got nil")
	}
}

func TestQueryPrometheusRangeHTTPError(t *testing.T) {
	fakePrometheus(t, "", "")

	if _, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m"); err == nil {
		t.Error("want error for HTTP 500, got nil")
	}
}

func TestQueryPrometheusReturnsInstantValue(t *testing.T) {
	fakePrometheus(t, "", promInstant(0.75))

	v, err := queryPrometheus("q")

	if err != nil || v != 0.75 {
		t.Errorf("got (%v, %v), want (0.75, nil)", v, err)
	}
}

func TestQueryPrometheusEmptyResultIsError(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)

	if _, err := queryPrometheus("q"); err == nil {
		t.Error("want error for empty instant result, got nil")
	}
}

func TestQueryPrometheusRangeTimesOut(t *testing.T) {
	useClient(t, &promClient, 50*time.Millisecond)
	old := prometheusURL
	t.Cleanup(func() { prometheusURL = old })
	prometheusURL = fakeSlowServer(t, 5*time.Second)

	start := time.Now()
	_, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, want the 50ms client timeout to apply", elapsed)
	}
}

func TestQueryPrometheusRangeSkipsShortRows(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` +
		`[1700000000,"1"],[1700000300],[]]}]}}`
	fakePrometheus(t, body, "")

	pts, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil || len(pts) != 1 || pts[0].Y != 1 {
		t.Errorf("got (%+v, %v), want only the one complete row", pts, err)
	}
}

func TestQueryPrometheusShortValueIsError(t *testing.T) {
	fakePrometheus(t, "", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000]}]}}`)

	if _, err := queryPrometheus("q"); err == nil {
		t.Error("want error for a value without a sample, got nil")
	}
}
