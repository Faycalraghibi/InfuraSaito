package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
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
		store, err := openStore(context.Background(), url)
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
		recordForecasts(context.Background())
		if alerts != nil {
			alerts.evaluate()
		}
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
