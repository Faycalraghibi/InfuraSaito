package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

var prometheusURL string

func main() {
	prometheusURL = os.Getenv("PROMETHEUS_URL")
	if prometheusURL == "" {
		prometheusURL = "http://localhost:9090"
		log.Println("PROMETHEUS_URL not set, defaulting to", prometheusURL)
	}

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		log.Println("API_KEY not set, /api/ routes are unauthenticated")
	}
	router := newRouter(apiKey, rateLimiterFromEnv())

	if url := os.Getenv("DATABASE_URL"); url != "" {
		// A set but unreachable DATABASE_URL stops startup, like an invalid alert config.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		store, err := openStore(ctx, url)
		cancel()
		if err != nil {
			log.Fatalf("Forecast storage: %v", err)
		}
		forecasts = store
		log.Println("Recording forecasts after each training run")
	} else {
		log.Println("DATABASE_URL not set, forecasts are not stored (accuracy endpoint disabled)")
	}

	alerts, err := alerterFromEnv()
	if err != nil {
		log.Fatalf("Invalid alert configuration: %v", err)
	}
	if alerts != nil {
		log.Printf("Alerting on %d rule(s) after each training run", len(alerts.rules))
	}
	afterTraining := func() {
		// Alerts first: they don't depend on the database, so a slow one can't delay them.
		if alerts != nil {
			alerts.evaluate()
		}
		// Bound the database work so a stalled Postgres can't block the trainer loop.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		recordForecasts(ctx)
	}

	if interval := trainIntervalFromEnv(); interval > 0 {
		log.Printf("Background training every %s", interval)
		startTrainer(context.Background(), interval, afterTraining)
	} else {
		log.Println("Background training disabled (TRAIN_INTERVAL=0)")
		if alerts != nil || forecasts != nil {
			log.Println("Alerts and forecast recording are configured but never run: they run after background training")
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting Go API on port %s", port)
	if err := http.ListenAndServe(":"+port, router); err != nil {
		log.Fatal(err)
	}
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
