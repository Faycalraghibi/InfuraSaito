package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	Instance       string      `json:"instance,omitempty"`
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

// errNoCachedModel means the AI service has no trained model for the metric and instance yet.
var errNoCachedModel = errors.New("no cached model")

// aiStatusError is a non-success HTTP status from the AI service.
type aiStatusError struct {
	status int
	body   []byte
}

func (e *aiStatusError) Error() string {
	return fmt.Sprintf("AI service returned status %d: %s", e.status, e.body)
}

type TrainRequest struct {
	MetricName string      `json:"metric_name"`
	Instance   string      `json:"instance,omitempty"`
	History    []DataPoint `json:"history"`
}

type ForecastRequest struct {
	MetricName     string `json:"metric_name"`
	Instance       string `json:"instance,omitempty"`
	HorizonMinutes int    `json:"horizon_minutes"`
}

// postAI POSTs body as JSON to an AI service path and returns the response body and status.
func postAI(path string, body any) ([]byte, int, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	if err := aiBreaker.allow(); err != nil {
		return nil, 0, err
	}

	resp, err := aiClient.Post(aiServiceURL+path, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		aiBreaker.record(true)
		return nil, 0, fmt.Errorf("failed to call AI service: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		aiBreaker.record(true)
		return nil, resp.StatusCode, err
	}
	// Only server errors count against the AI service; 4xx (e.g. no cached model) do not.
	aiBreaker.record(resp.StatusCode >= 500)
	return respBody, resp.StatusCode, nil
}

// callForecastModel trains on history and predicts in one call (/predict).
func callForecastModel(metric, instance string, history []DataPoint, horizonMin int) ([]byte, error) {
	body, status, err := postAI("/predict", PredictRequest{
		MetricName:     metric,
		Instance:       instance,
		History:        history,
		HorizonMinutes: horizonMin,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("AI service returned status %d: %s", status, body)
	}
	return body, nil
}

// trainModel fits and caches a model for the metric and instance (/train).
func trainModel(metric, instance string, history []DataPoint) error {
	body, status, err := postAI("/train", TrainRequest{MetricName: metric, Instance: instance, History: history})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("AI service returned status %d: %s", status, body)
	}
	return nil
}

// forecastFromCache predicts from the cached model (/forecast). It returns errNoCachedModel
// when the AI service has not trained one yet.
func forecastFromCache(metric, instance string, horizonMin int) ([]byte, error) {
	body, status, err := postAI("/forecast", ForecastRequest{MetricName: metric, Instance: instance, HorizonMinutes: horizonMin})
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return body, nil
	case http.StatusNotFound:
		return nil, errNoCachedModel
	}
	return nil, &aiStatusError{status: status, body: body}
}
