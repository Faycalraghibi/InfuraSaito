import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func forecastHandler(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		metric = "cpu"
	}

	horizonStr := r.URL.Query().Get("horizon_minutes")
	horizonMin := 60
	if h, err := strconv.Atoi(horizonStr); err == nil && h > 0 {
		horizonMin = h
	}

	// 1. Fetch historical data from Prometheus (last 14 days, 5m resolution)
	// Query: average CPU usage across all nodes
	query := `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`
	
	end := time.Now()
	start := end.Add(-14 * 24 * time.Hour)
	step := "5m"

	history, err := queryPrometheusRange(query, start, end, step)
	if err != nil {
		http.Error(w, "Failed to fetch historical data: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 1.5 Handle Cold Start (Insufficient History)
	if len(history) < 2 {
		fallbackJSON := fmt.Sprintf(`{"metric": "%s", "horizon_minutes": %d, "confidence": "none - insufficient history", "predictions": [{"time": "%s", "value": 30.0, "lower": 0.0, "upper": 100.0}]}`, metric, horizonMin, time.Now().UTC().Format(time.RFC3339))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fallbackJSON))
		return
	}

	// 2. Pass data to AI Service
	aiResponseBytes, err := callForecastModel(metric, history, horizonMin)
	if err != nil {
		http.Error(w, "AI Forecasting failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// 3. Return original AI JSON back to client
	w.Header().Set("Content-Type", "application/json")
	w.Write(aiResponseBytes)
}

func currentMetricsHandler(w http.ResponseWriter, r *http.Request) {
	// Query CPU usage over the last 1 minute from Prometheus
	query := `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[1m])) * 100)`
	
	val, err := queryPrometheus(query)
	if err != nil {
		http.Error(w, "Failed to fetch metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"metric": "cpu",
		"value":  val,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
