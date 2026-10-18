package main

import (
	"testing"
	"time"
)

func TestQueryPrometheusRangeParsesValues(t *testing.T) {
	fakePrometheus(t, promRange(1.5, 2.5), "")

	series, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pts := firstSeriesPoints(t, series)
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

	series, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pts := firstSeriesPoints(t, series)
	if len(pts) != 2 || pts[0].Y != 1 || pts[1].Y != 3 {
		t.Errorf("got %+v, want only the two well-formed rows", pts)
	}
}

func TestQueryPrometheusRangeEmptyResultIsNotAnError(t *testing.T) {
	fakePrometheus(t, promRange(), "")

	series, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil || len(series) != 0 {
		t.Errorf("got (%v, %v), want (empty, nil)", series, err)
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

	series, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pts := firstSeriesPoints(t, series)
	if len(pts) != 1 || pts[0].Y != 1 {
		t.Errorf("got %+v, want only the one complete row", pts)
	}
}

func TestQueryPrometheusShortValueIsError(t *testing.T) {
	fakePrometheus(t, "", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000]}]}}`)

	if _, err := queryPrometheus("q"); err == nil {
		t.Error("want error for a value without a sample, got nil")
	}
}

// firstSeriesPoints fails the test unless there is exactly one series, and returns its points.
func firstSeriesPoints(t *testing.T, series []promSeries) []DataPoint {
	t.Helper()
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	return series[0].Points
}

func TestQueryPrometheusRangeReturnsEverySeries(t *testing.T) {
	fakePrometheus(t, promMatrix(promSeriesJSON("a:9100", 1, 2), promSeriesJSON("b:9100", 3)), "")

	series, err := queryPrometheusRange("q", time.Unix(0, 0), time.Unix(600, 0), "5m")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(series) != 2 || series[0].Instance != "a:9100" || series[1].Instance != "b:9100" {
		t.Fatalf("series = %+v, want a:9100 and b:9100", series)
	}
	if len(series[0].Points) != 2 || len(series[1].Points) != 1 || series[1].Points[0].Y != 3 {
		t.Errorf("points = %+v / %+v, want 2 and 1 (y=3)", series[0].Points, series[1].Points)
	}
}

func TestQueryPrometheusInstantReturnsEverySample(t *testing.T) {
	fakePrometheus(t, "", promVector(promSampleJSON("a:9100", 1.5), promSampleJSON("b:9100", 2.5)))

	samples, err := queryPrometheusInstant("q")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 2 || samples[0] != (promSample{"a:9100", 1.5}) || samples[1] != (promSample{"b:9100", 2.5}) {
		t.Errorf("samples = %+v, want a:9100=1.5 and b:9100=2.5", samples)
	}
}

func TestQueryPrometheusInstantEmptyIsNotAnError(t *testing.T) {
	fakePrometheus(t, "", promEmptyVector)

	samples, err := queryPrometheusInstant("q")

	if err != nil || len(samples) != 0 {
		t.Errorf("got (%v, %v), want (empty, nil)", samples, err)
	}
}
