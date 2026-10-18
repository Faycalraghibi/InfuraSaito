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

	alerts, err := alerterFromEnv()
	if err != nil {
		log.Fatalf("Invalid alert configuration: %v", err)
	}
	var afterTraining func()
	if alerts != nil {
		log.Printf("Alerting on %d rule(s) after each training run", len(alerts.rules))
		afterTraining = alerts.evaluate
	}

	if interval := trainIntervalFromEnv(); interval > 0 {
		log.Printf("Background training every %s", interval)
		startTrainer(context.Background(), interval, afterTraining)
	} else {
		log.Println("Background training disabled (TRAIN_INTERVAL=0)")
		if alerts != nil {
			log.Println("Alert rules are configured but never evaluated: they run after background training")
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
