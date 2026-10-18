package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

// Clients for outbound calls. http.Get/http.Post use a client with no timeout,
// so a hung dependency would hang the request forever.
var (
	promClient = &http.Client{Timeout: durationFromEnv("PROM_TIMEOUT", 10*time.Second)}
	aiClient   = &http.Client{Timeout: durationFromEnv("AI_TIMEOUT", 60*time.Second)}
)

// durationFromEnv parses envVar as a Go duration (e.g. "30s"), returning def when it
// is unset, invalid or not positive.
func durationFromEnv(envVar string, def time.Duration) time.Duration {
	v := os.Getenv(envVar)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("invalid %s=%q, using %s", envVar, v, def)
		return def
	}
	return d
}
