package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// openTestStore connects to TEST_DATABASE_URL (a disposable Postgres database), or skips.
func openTestStore(t *testing.T) *pgStore {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := openStore(ctx, url)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(s.close)
	if _, err := s.pool.Exec(ctx, "TRUNCATE forecasts"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestStoreSavesAndLoadsPredictionsByWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	err := s.save(ctx, "cpu", "a:9100", []storedPrediction{
		{Target: base, Value: 1, Lower: 0, Upper: 2},
		{Target: base.Add(time.Hour), Value: 3, Lower: 2, Upper: 4},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, other := range []struct{ metric, instance string }{{"cpu", "b:9100"}, {"memory", "a:9100"}} {
		if err := s.save(ctx, other.metric, other.instance, []storedPrediction{{Target: base, Value: 9}}); err != nil {
			t.Fatalf("save %s/%s: %v", other.metric, other.instance, err)
		}
	}

	got, err := s.load(ctx, "cpu", "a:9100", base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Value != 1 || got[0].Lower != 0 || got[0].Upper != 2 || !got[0].Target.Equal(base) {
		t.Errorf("load = %+v, want only the cpu/a:9100 prediction at %s", got, base)
	}
}

func TestStoreDeletesPredictionsPastRetention(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Duration(retentionDays+5) * 24 * time.Hour)

	// Insert expired rows directly: save itself would delete them in the same transaction.
	for _, metric := range []string{"cpu", "memory"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO forecasts (metric, instance, target_time, value, lower, upper)
			VALUES ($1, 'a:9100', $2, 1, 0, 2)`, metric, old); err != nil {
			t.Fatalf("insert old %s: %v", metric, err)
		}
	}
	if err := s.save(ctx, "cpu", "a:9100", []storedPrediction{{Target: now, Value: 2}}); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	got, err := s.load(ctx, "cpu", "a:9100", old.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Value != 2 {
		t.Errorf("cpu/a:9100 = %+v, want only the fresh prediction", got)
	}
	// Retention is per series: memory/a:9100 has not been saved since, so it is untouched.
	other, _ := s.load(ctx, "memory", "a:9100", old.Add(-time.Hour), now.Add(time.Hour))
	if len(other) != 1 {
		t.Errorf("memory/a:9100 = %+v, want its old prediction kept until that series is saved again", other)
	}
}

func TestOpenStoreIsIdempotent(t *testing.T) {
	openTestStore(t)

	// Opening again re-runs the migration against the existing table.
	s, err := openStore(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("second openStore: %v", err)
	}
	s.close()
}
