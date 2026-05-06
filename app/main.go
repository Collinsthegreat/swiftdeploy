package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	mode       = getEnv("MODE", "stable")
	appVersion = getEnv("APP_VERSION", "1.0.0")
	appPort    = getEnv("APP_PORT", "3000")
	startTime  = time.Now()
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type ChaosState struct {
	mu       sync.Mutex
	Active   bool
	Mode     string
	Duration int
	Rate     float64
}

var chaos = &ChaosState{}

const recentMetricsWindow = 30 * time.Second

type requestEvent struct {
	at       time.Time
	method   string
	path     string
	status   int
	duration float64
}

type MetricsStore struct {
	mu            sync.Mutex
	requestCounts map[string]*int64
	durations     map[string][]float64
	buckets       []float64
	events        []requestEvent
}

var metrics = &MetricsStore{
	requestCounts: make(map[string]*int64),
	durations:     make(map[string][]float64),
	buckets:       []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
}

func (m *MetricsStore) RecordRequest(method, path string, status int, duration float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s|%s|%d", method, path, status)
	if _, ok := m.requestCounts[key]; !ok {
		var c int64
		m.requestCounts[key] = &c
	}
	atomic.AddInt64(m.requestCounts[key], 1)
	m.durations[path] = append(m.durations[path], duration)
	now := time.Now()
	m.events = append(m.events, requestEvent{
		at:       now,
		method:   method,
		path:     path,
		status:   status,
		duration: duration,
	})
	m.trimRecentEventsLocked(now)
}

func (m *MetricsStore) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var sb strings.Builder
	now := time.Now()
	m.trimRecentEventsLocked(now)

	sb.WriteString("# HELP http_requests_total Total HTTP requests\n")
	sb.WriteString("# TYPE http_requests_total counter\n")
	keys := make([]string, 0, len(m.requestCounts))
	for key := range m.requestCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		count := m.requestCounts[key]
		parts := strings.SplitN(key, "|", 3)
		if len(parts) == 3 {
			fmt.Fprintf(&sb,
				"http_requests_total{method=%q,path=%q,status_code=%q} %d\n",
				parts[0], parts[1], parts[2], atomic.LoadInt64(count),
			)
		}
	}

	sb.WriteString("# HELP http_request_duration_seconds Request duration in seconds\n")
	sb.WriteString("# TYPE http_request_duration_seconds histogram\n")
	paths := make([]string, 0, len(m.durations))
	for path := range m.durations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		durs := m.durations[path]
		sort.Float64s(durs)
		total := 0.0
		for _, d := range durs {
			total += d
		}
		for _, le := range m.buckets {
			count := 0
			for _, d := range durs {
				if d <= le {
					count++
				}
			}
			fmt.Fprintf(&sb,
				"http_request_duration_seconds_bucket{le=\"%g\",path=%q} %d\n",
				le, path, count,
			)
		}
		fmt.Fprintf(&sb, "http_request_duration_seconds_bucket{le=\"+Inf\",path=%q} %d\n", path, len(durs))
		fmt.Fprintf(&sb, "http_request_duration_seconds_sum{path=%q} %g\n", path, total)
		fmt.Fprintf(&sb, "http_request_duration_seconds_count{path=%q} %d\n", path, len(durs))
	}

	m.renderRecentWindowLocked(&sb)

	sb.WriteString("# HELP app_uptime_seconds Application uptime\n")
	sb.WriteString("# TYPE app_uptime_seconds gauge\n")
	fmt.Fprintf(&sb, "app_uptime_seconds %g\n", time.Since(startTime).Seconds())

	sb.WriteString("# HELP app_mode Current deployment mode (0=stable,1=canary)\n")
	sb.WriteString("# TYPE app_mode gauge\n")
	modeVal := 0
	if mode == "canary" {
		modeVal = 1
	}
	fmt.Fprintf(&sb, "app_mode{mode=%q} %d\n", mode, modeVal)

	sb.WriteString("# HELP chaos_active Chaos state (0=none,1=slow,2=error)\n")
	sb.WriteString("# TYPE chaos_active gauge\n")
	chaosVal := 0
	chaos.mu.Lock()
	if chaos.Active {
		switch chaos.Mode {
		case "slow":
			chaosVal = 1
		case "error":
			chaosVal = 2
		}
	}
	chaos.mu.Unlock()
	fmt.Fprintf(&sb, "chaos_active %d\n", chaosVal)

	return sb.String()
}

func (m *MetricsStore) trimRecentEventsLocked(now time.Time) {
	cutoff := now.Add(-recentMetricsWindow)
	keepFrom := 0
	for keepFrom < len(m.events) && m.events[keepFrom].at.Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		copy(m.events, m.events[keepFrom:])
		m.events = m.events[:len(m.events)-keepFrom]
	}
}

func (m *MetricsStore) renderRecentWindowLocked(sb *strings.Builder) {
	recentCounts := map[string]int{}
	recentDurations := map[string][]float64{}
	for _, event := range m.events {
		key := fmt.Sprintf("%s|%s|%d", event.method, event.path, event.status)
		recentCounts[key]++
		recentDurations[event.path] = append(recentDurations[event.path], event.duration)
	}

	sb.WriteString("# HELP swiftdeploy_recent_http_requests_total HTTP requests observed during the last 30 seconds\n")
	sb.WriteString("# TYPE swiftdeploy_recent_http_requests_total gauge\n")
	countKeys := make([]string, 0, len(recentCounts))
	for key := range recentCounts {
		countKeys = append(countKeys, key)
	}
	sort.Strings(countKeys)
	for _, key := range countKeys {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) == 3 {
			fmt.Fprintf(sb,
				"swiftdeploy_recent_http_requests_total{method=%q,path=%q,status_code=%q} %d\n",
				parts[0], parts[1], parts[2], recentCounts[key],
			)
		}
	}

	sb.WriteString("# HELP swiftdeploy_recent_request_duration_seconds Request duration observed during the last 30 seconds\n")
	sb.WriteString("# TYPE swiftdeploy_recent_request_duration_seconds histogram\n")
	paths := make([]string, 0, len(recentDurations))
	for path := range recentDurations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		durs := recentDurations[path]
		sort.Float64s(durs)
		total := 0.0
		for _, d := range durs {
			total += d
		}
		for _, le := range m.buckets {
			count := 0
			for _, d := range durs {
				if d <= le {
					count++
				}
			}
			fmt.Fprintf(sb,
				"swiftdeploy_recent_request_duration_seconds_bucket{le=\"%g\",path=%q} %d\n",
				le, path, count,
			)
		}
		fmt.Fprintf(sb, "swiftdeploy_recent_request_duration_seconds_bucket{le=\"+Inf\",path=%q} %d\n", path, len(durs))
		fmt.Fprintf(sb, "swiftdeploy_recent_request_duration_seconds_sum{path=%q} %g\n", path, total)
		fmt.Fprintf(sb, "swiftdeploy_recent_request_duration_seconds_count{path=%q} %d\n", path, len(durs))
	}
}

func p99FromHistogram(durs []float64) float64 {
	if len(durs) == 0 {
		return 0
	}
	sorted := make([]float64, len(durs))
	copy(sorted, durs)
	sort.Float64s(sorted)
	idx := int(math.Ceil(float64(len(sorted))*0.99)) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func withMetrics(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		chaos.mu.Lock()
		active := chaos.Active
		cMode := chaos.Mode
		duration := chaos.Duration
		rate := chaos.Rate
		chaos.mu.Unlock()

		if active && path != "/chaos" && path != "/healthz" && path != "/metrics" {
			if cMode == "slow" && duration > 0 {
				time.Sleep(time.Duration(duration) * time.Second)
			} else if cMode == "error" && rand.Float64() < rate {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Deployed-By", "swiftdeploy")
				if mode == "canary" {
					w.Header().Set("X-Mode", "canary")
				}
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": "Chaos-induced error",
					"mode":  "error",
					"rate":  rate,
				})
				metrics.RecordRequest(r.Method, path, http.StatusInternalServerError, time.Since(start).Seconds())
				return
			}
		}

		if mode == "canary" {
			w.Header().Set("X-Mode", "canary")
		}
		w.Header().Set("X-Deployed-By", "swiftdeploy")

		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rw, r)
		metrics.RecordRequest(r.Method, path, rw.status, time.Since(start).Seconds())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Not found"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message":        fmt.Sprintf("SwiftDeploy API is running in %s mode", mode),
		"mode":           mode,
		"version":        appVersion,
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"uptime_seconds": time.Since(startTime).Seconds(),
	})
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":         "ok",
		"mode":           mode,
		"version":        appVersion,
		"uptime_seconds": time.Since(startTime).Seconds(),
	})
}

func handleChaos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if mode != "canary" {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Chaos endpoint only available in canary mode",
		})
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	chaosMode, _ := body["mode"].(string)
	chaos.mu.Lock()
	defer chaos.mu.Unlock()
	switch chaosMode {
	case "recover":
		chaos.Active = false
		chaos.Mode = ""
		chaos.Duration = 0
		chaos.Rate = 0
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Chaos cancelled. Service recovered."})
	case "slow":
		dur := 5
		if d, ok := body["duration"].(float64); ok {
			dur = int(d)
		} else if d, ok := body["duration"].(string); ok {
			if parsed, err := strconv.Atoi(d); err == nil {
				dur = parsed
			}
		}
		chaos.Active = true
		chaos.Mode = "slow"
		chaos.Duration = dur
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Slow chaos activated", "duration_seconds": dur,
		})
	case "error":
		rate := 0.5
		if rt, ok := body["rate"].(float64); ok {
			rate = rt
		} else if rt, ok := body["rate"].(string); ok {
			if parsed, err := strconv.ParseFloat(rt, 64); err == nil {
				rate = parsed
			}
		}
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		chaos.Active = true
		chaos.Mode = "error"
		chaos.Rate = rate
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Error chaos activated", "error_rate": rate,
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Unknown chaos mode: %s", chaosMode),
		})
	}
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, metrics.Render())
}

func main() {
	rand.Seed(time.Now().UnixNano())

	mux := http.NewServeMux()
	mux.HandleFunc("/", withMetrics("/", handleRoot))
	mux.HandleFunc("/healthz", withMetrics("/healthz", handleHealthz))
	mux.HandleFunc("/chaos", withMetrics("/chaos", handleChaos))
	mux.HandleFunc("/metrics", withMetrics("/metrics", handleMetrics))

	addr := "0.0.0.0:" + appPort
	log.Printf("SwiftDeploy API starting on %s (mode=%s version=%s)", addr, mode, appVersion)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
