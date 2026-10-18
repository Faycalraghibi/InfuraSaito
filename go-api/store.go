package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storedPrediction is one predicted point for a metric and instance.
type storedPrediction struct {
	Target time.Time
	Value  float64
	Lower  float64
	Upper  float64
}

// predictionStore persists forecasts so they can be compared with actual values later.
type predictionStore interface {
	save(ctx context.Context, metric, instance string, preds []storedPrediction) error
	load(ctx context.Context, metric, instance string, from, to time.Time) ([]storedPrediction, error)
}

// forecasts is the configured store; nil when DATABASE_URL is not set.
var forecasts predictionStore

// migrations create the schema; each statement is idempotent.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS forecasts (
		id          BIGSERIAL PRIMARY KEY,
		metric      TEXT NOT NULL,
		instance    TEXT NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		target_time TIMESTAMPTZ NOT NULL,
		value       DOUBLE PRECISION NOT NULL,
		lower       DOUBLE PRECISION NOT NULL,
		upper       DOUBLE PRECISION NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS forecasts_lookup ON forecasts (metric, instance, target_time)`,
}

// pgStore is a predictionStore backed by Postgres.
type pgStore struct {
	pool *pgxpool.Pool
}

// openStore connects to Postgres at url and applies the migrations.
func openStore(ctx context.Context, url string) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	for _, stmt := range migrations {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrating the database: %w", err)
		}
	}
	return &pgStore{pool: pool}, nil
}

func (s *pgStore) close() { s.pool.Close() }

func (s *pgStore) save(ctx context.Context, metric, instance string, preds []storedPrediction) error {
	rows := make([][]any, len(preds))
	for i, p := range preds {
		rows[i] = []any{metric, instance, p.Target, p.Value, p.Lower, p.Upper}
	}
	_, err := s.pool.CopyFrom(ctx, pgx.Identifier{"forecasts"},
		[]string{"metric", "instance", "target_time", "value", "lower", "upper"}, pgx.CopyFromRows(rows))
	return err
}

func (s *pgStore) load(ctx context.Context, metric, instance string, from, to time.Time) ([]storedPrediction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT target_time, value, lower, upper FROM forecasts
		WHERE metric = $1 AND instance = $2 AND target_time BETWEEN $3 AND $4
		ORDER BY target_time`, metric, instance, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (storedPrediction, error) {
		var p storedPrediction
		err := row.Scan(&p.Target, &p.Value, &p.Lower, &p.Upper)
		return p, err
	})
}
