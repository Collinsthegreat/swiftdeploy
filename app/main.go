package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	mode       = strings.ToLower(getenv("MODE", "stable"))
	appVersion = getenv("APP_VERSION", "1.0.0")
	appPort    = getenv("APP_PORT", "3000")
	startTime  = time.Now()
)

type chaosConfig struct {
	Active    bool
	Mode      string
	Duration  int
	Rate      float64
	StartedAt *time.Time
}

var (
	chaosMu    sync.Mutex
	chaosState = chaosConfig{}
)

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func uptimeSeconds() float64 {
	return float64(time.Since(startTime).Milliseconds()) / 1000
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("failed to encode JSON response: %v", err)
	}
}

func commonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "canary" {
			w.Header().Set("X-Mode", "canary")
		}
		w.Header().Set("X-Deployed-By", "swiftdeploy")
		next.ServeHTTP(w, r)
	})
}

func chaosMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chaosMu.Lock()
		active := chaosState.Active
		chaosMode := chaosState.Mode
		duration := chaosState.Duration
		rate := chaosState.Rate
		chaosMu.Unlock()

		if active && r.URL.Path != "/chaos" && r.URL.Path != "/healthz" {
			switch chaosMode {
			case "slow":
				if duration > 0 {
					time.Sleep(time.Duration(duration) * time.Second)
				}
			case "error":
				if rand.Float64() < rate {
					writeJSON(w, http.StatusInternalServerError, map[string]any{
						"error": "Chaos-induced error",
						"mode":  "error",
						"rate":  rate,
					})
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "Not found",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":        fmt.Sprintf("SwiftDeploy API is running in %s mode", mode),
		"mode":           mode,
		"version":        appVersion,
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"uptime_seconds": uptimeSeconds(),
	})
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"mode":           mode,
		"version":        appVersion,
		"uptime_seconds": uptimeSeconds(),
	})
}

func chaosHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if mode != "canary" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "Chaos endpoint only available in canary mode",
		})
		return
	}

	var body map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Invalid JSON request body",
		})
		return
	}

	chaosMode, _ := body["mode"].(string)
	chaosMu.Lock()
	defer chaosMu.Unlock()

	switch chaosMode {
	case "recover":
		chaosState = chaosConfig{}
		writeJSON(w, http.StatusOK, map[string]any{
			"message": "Chaos cancelled. Service recovered.",
		})
	case "slow":
		duration := intFromAny(body["duration"], 5)
		now := time.Now()
		chaosState = chaosConfig{Active: true, Mode: "slow", Duration: duration, StartedAt: &now}
		writeJSON(w, http.StatusOK, map[string]any{
			"message":          "Slow chaos activated",
			"duration_seconds": duration,
		})
	case "error":
		rate := floatFromAny(body["rate"], 0.5)
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		now := time.Now()
		chaosState = chaosConfig{Active: true, Mode: "error", Rate: rate, StartedAt: &now}
		writeJSON(w, http.StatusOK, map[string]any{
			"message":    "Error chaos activated",
			"error_rate": rate,
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("Unknown chaos mode: %s. Use: slow, error, recover", chaosMode),
		})
	}
}

func intFromAny(value any, fallback int) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func floatFromAny(value any, fallback float64) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case string:
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"error": "Method not allowed",
	})
}

func main() {
	rand.Seed(time.Now().UnixNano())

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/chaos", chaosHandler)

	addr := "0.0.0.0:" + appPort
	log.Printf("SwiftDeploy API starting on %s in %s mode", addr, mode)
	if err := http.ListenAndServe(addr, commonHeaders(chaosMiddleware(mux))); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
