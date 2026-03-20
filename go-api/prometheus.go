package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Minimal struct to parse Prometheus HTTP API response
type PromResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value  []interface{}   `json:"value"` // For instant queries
			Values [][]interface{} `json:"values"` // For range queries
		} `json:"result"`
	} `json:"data"`
}

// queryPrometheusRange runs a PromQL range query and returns a list of DataPoints
func queryPrometheusRange(query string, start, end time.Time, step string) ([]DataPoint, error) {
	apiQuery := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%s",
		prometheusURL, url.QueryEscape(query), start.Unix(), end.Unix(), step)

	resp, err := http.Get(apiQuery)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Prometheus returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var promResp PromResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, err
	}

	if promResp.Status != "success" || len(promResp.Data.Result) == 0 {
		return nil, fmt.Errorf("no data returned from Prometheus")
	}

	var dataPoints []DataPoint
	for _, valArr := range promResp.Data.Result[0].Values {
		// valArr is [timestamp_float, "value_string"]
		tsFloat, ok1 := valArr[0].(float64)
		valStr, ok2 := valArr[1].(string)
		if !ok1 || !ok2 {
			continue // Skip malformed rows
		}

		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}

		// Convert Unix timestamp to ISO8601 string for Prophet
		ts := time.Unix(int64(tsFloat), 0).UTC().Format(time.RFC3339)
		dataPoints = append(dataPoints, DataPoint{Ds: ts, Y: val})
	}

	return dataPoints, nil
}

// queryPrometheus runs a PromQL query and returns the first float value
func queryPrometheus(query string) (float64, error) {
	apiQuery := fmt.Sprintf("%s/api/v1/query?query=%s", prometheusURL, url.QueryEscape(query))
	
	resp, err := http.Get(apiQuery)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("Prometheus returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var promResp PromResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return 0, err
	}

	if promResp.Status != "success" || len(promResp.Data.Result) == 0 {
		return 0, fmt.Errorf("no data returned from Prometheus")
	}

	// Value is typically [timestamp, "string_value"]
	valStr, ok := promResp.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value format from Prometheus")
	}

	return strconv.ParseFloat(valStr, 64)
}
