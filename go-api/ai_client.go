package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type DataPoint struct {
	Ds string  `json:"ds"`
	Y  float64 `json:"y"`
}

type PredictRequest struct {
	MetricName     string      `json:"metric_name"`
	History        []DataPoint `json:"history"`
	HorizonMinutes int         `json:"horizon_minutes"`
}

var aiServiceURL string

func init() {
	aiServiceURL = os.Getenv("AI_SERVICE_URL")
	if aiServiceURL == "" {
		aiServiceURL = "http://localhost:5000"
	}
}

func callForecastModel(metric string, history []DataPoint, horizonMin int) ([]byte, error) {
	reqBody := PredictRequest{
		MetricName:     metric,
		History:        history,
		HorizonMinutes: horizonMin,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := http.Post(aiServiceURL+"/predict", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to call AI service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("AI service returned status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
