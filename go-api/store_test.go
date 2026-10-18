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
	s.save(ctx, "cpu", "b:9100", []storedPrediction{{Target: base, Value: 9}})
	s.save(ctx, "memory", "a:9100", []storedPrediction{{Target: base, Value: 9}})

	got, err := s.load(ctx, "cpu", "a:9100", base.Add(-time.Minute), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Value != 1 || got[0].Lower != 0 || got[0].Upper != 2 || !got[0].Target.Equal(base) {
		t.Errorf("load = %+v, want only the cpu/a:9100 prediction at %s", got, base)
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
