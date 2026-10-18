package main

import (
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
