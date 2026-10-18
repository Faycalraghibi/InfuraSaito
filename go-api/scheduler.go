package main

import (
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"time"
)

// History window and resolution used for both training and on-request forecasts.
const (
	historyWindow = 14 * 24 * time.Hour
	historyStep   = "5m"
)

// trainAll fetches history for every metric and instance and trains a cached model for each
// series with at least 2 points. Failures are logged and skipped. It returns the number trained.
func trainAll() int {
	end := time.Now()
	start := end.Add(-historyWindow)

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)

	trained := 0
	defer func() {
		modelsTrained.Set(float64(trained))
		lastTraining.SetToCurrentTime()
	}()
	for _, name := range names {
		seriesList, err := queryPrometheusRange(metrics[name].rangeQuery, start, end, historyStep)
		if err != nil {
			log.Printf("training %s: fetching history failed: %v", name, err)
			continue
		}
		for _, series := range seriesList {
			if len(series.Points) < 2 {
				continue
			}
			if err := trainModel(name, series.Instance, series.Points); err != nil {
				var openErr *circuitOpenError
				if errors.As(err, &openErr) {
					log.Printf("training stopped: %v", err)
					return trained
				}
				log.Printf("training %s/%s failed: %v", name, series.Instance, err)
				continue
			}
			trained++
		}
	}
	return trained
}

// startTrainer runs trainAll now and then every interval until ctx is cancelled, calling
// afterRun (if not nil) after each run. Until the first run trains something (e.g.
// Prometheus has no history yet), it retries every min(interval, firstTrainingRetry).
func startTrainer(ctx context.Context, interval time.Duration, afterRun func()) {
	retry := firstTrainingRetry // read once, before the goroutine starts
	go func() {
		trainedOnce := false
		for {
			n := trainAll()
			log.Printf("background training: %d models trained", n)
			if afterRun != nil {
				afterRun()
			}
			trainedOnce = trainedOnce || n > 0
			wait := interval
			if !trainedOnce {
				wait = min(interval, retry)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// trainIntervalFromEnv reads TRAIN_INTERVAL (a Go duration, default 15m); "0" disables
// background training.
func trainIntervalFromEnv() time.Duration {
	if os.Getenv("TRAIN_INTERVAL") == "0" {
		return 0
	}
	return durationFromEnv("TRAIN_INTERVAL", 15*time.Minute)
}

// firstTrainingRetry is how soon to retry while no model has been trained yet.
var firstTrainingRetry = time.Minute
