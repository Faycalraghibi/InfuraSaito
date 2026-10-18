package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

type PromResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// promSeries is one range-query series: its instance label and parsed points.
type promSeries struct {
	Instance string
	Points   []DataPoint
}

// promSample is one instant-query sample: its instance label and value.
type promSample struct {
	Instance string
	Value    float64
}

// getPrometheus performs a Prometheus API request and decodes a successful response.
func getPrometheus(apiQuery string) (*PromResponse, error) {
	resp, err := promClient.Get(apiQuery)
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

	if promResp.Status != "success" {
		return nil, fmt.Errorf("Prometheus query failed: status %s", promResp.Status)
	}
	return &promResp, nil
}

// queryPrometheusRange returns every series of a range query with its parsed points.
func queryPrometheusRange(query string, start, end time.Time, step string) ([]promSeries, error) {
	apiQuery := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%s",
		prometheusURL, url.QueryEscape(query), start.Unix(), end.Unix(), step)

	promResp, err := getPrometheus(apiQuery)
	if err != nil {
		return nil, err
	}

	series := make([]promSeries, 0, len(promResp.Data.Result))
	for _, result := range promResp.Data.Result {
		series = append(series, promSeries{
			Instance: result.Metric["instance"],
			Points:   parseRangeValues(result.Values),
		})
	}
	return series, nil
}

// parseRangeValues converts [timestamp, "value"] rows into DataPoints, skipping malformed rows.
func parseRangeValues(rows [][]interface{}) []DataPoint {
	points := []DataPoint{}
	for _, row := range rows {
		if len(row) < 2 {
			continue // Skip rows without both timestamp and value
		}
		tsFloat, ok1 := row[0].(float64)
		valStr, ok2 := row[1].(string)
		if !ok1 || !ok2 {
			continue // Skip malformed rows
		}

		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}

		// Convert Unix timestamp to ISO8601 string for Prophet
		ts := time.Unix(int64(tsFloat), 0).UTC().Format(time.RFC3339)
		points = append(points, DataPoint{Ds: ts, Y: val})
	}
	return points
}

// queryPrometheusInstant returns every sample of an instant query, skipping malformed ones.
func queryPrometheusInstant(query string) ([]promSample, error) {
	apiQuery := fmt.Sprintf("%s/api/v1/query?query=%s", prometheusURL, url.QueryEscape(query))

	promResp, err := getPrometheus(apiQuery)
	if err != nil {
		return nil, err
	}

	samples := []promSample{}
	for _, result := range promResp.Data.Result {
		// Value is [timestamp, "string_value"]
		if len(result.Value) < 2 {
			continue
		}
		valStr, ok := result.Value[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		samples = append(samples, promSample{Instance: result.Metric["instance"], Value: val})
	}
	return samples, nil
}

// queryPrometheus runs an instant query and returns its first value. Use it for
// queries that aggregate to a single series, such as max(up{...}).
func queryPrometheus(query string) (float64, error) {
	samples, err := queryPrometheusInstant(query)
	if err != nil {
		return 0, err
	}
	if len(samples) == 0 {
		return 0, fmt.Errorf("no data returned from Prometheus")
	}
	return samples[0].Value, nil
}
