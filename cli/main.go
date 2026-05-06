package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	manifestFile    = "manifest.yaml"
	nginxConfFile   = "nginx.conf"
	composeFile     = "docker-compose.yml"
	templatesDir    = "templates"
	historyFile     = "history.jsonl"
	auditReportFile = "audit_report.md"
	opaTimeout      = 5 * time.Second
)

const (
	colorGreen  = "\033[92m"
	colorRed    = "\033[91m"
	colorYellow = "\033[93m"
	colorBlue   = "\033[94m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

func ok(msg string)   { fmt.Printf("%sPASS %s%s\n", colorGreen, msg, colorReset) }
func fail(msg string) { fmt.Printf("%sFAIL %s%s\n", colorRed, msg, colorReset) }
func info(msg string) { fmt.Printf("%sINFO %s%s\n", colorBlue, msg, colorReset) }
func warn(msg string) { fmt.Printf("%sWARN %s%s\n", colorYellow, msg, colorReset) }
func hdr(msg string) {
	fmt.Printf("\n%s%s%s\n%s\n", colorBold, msg, colorReset, strings.Repeat("-", 50))
}

type Manifest struct {
	Services struct {
		Name                string `yaml:"name"`
		Image               string `yaml:"image"`
		Port                int    `yaml:"port"`
		Mode                string `yaml:"mode"`
		Version             string `yaml:"version"`
		RestartPolicy       string `yaml:"restart_policy"`
		LogVolume           string `yaml:"log_volume"`
		HealthcheckPath     string `yaml:"healthcheck_path"`
		HealthcheckInterval string `yaml:"healthcheck_interval"`
		HealthcheckTimeout  string `yaml:"healthcheck_timeout"`
		HealthcheckRetries  int    `yaml:"healthcheck_retries"`
	} `yaml:"services"`
	Nginx struct {
		Image        string `yaml:"image"`
		Port         int    `yaml:"port"`
		ProxyTimeout string `yaml:"proxy_timeout"`
		AccessLog    string `yaml:"access_log"`
	} `yaml:"nginx"`
	Network struct {
		Name       string `yaml:"name"`
		DriverType string `yaml:"driver_type"`
	} `yaml:"network"`
	OPA struct {
		Image       string `yaml:"image"`
		Port        int    `yaml:"port"`
		PoliciesDir string `yaml:"policies_dir"`
	} `yaml:"opa"`
}

func loadManifest() (*Manifest, error) {
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read manifest.yaml: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid YAML in manifest.yaml: %w", err)
	}
	return &m, nil
}

func normalizeTemplate(content string) string {
	replacements := map[string]string{
		"{{ services.name }}":                 "{{ .Services.Name }}",
		"{{ services.image }}":                "{{ .Services.Image }}",
		"{{ services.port }}":                 "{{ .Services.Port }}",
		"{{ services.mode }}":                 "{{ .Services.Mode }}",
		"{{ services.version }}":              "{{ .Services.Version }}",
		"{{ services.restart_policy }}":       "{{ .Services.RestartPolicy }}",
		"{{ services.log_volume }}":           "{{ .Services.LogVolume }}",
		"{{ services.healthcheck_path }}":     "{{ .Services.HealthcheckPath }}",
		"{{ services.healthcheck_interval }}": "{{ .Services.HealthcheckInterval }}",
		"{{ services.healthcheck_timeout }}":  "{{ .Services.HealthcheckTimeout }}",
		"{{ services.healthcheck_retries }}":  "{{ .Services.HealthcheckRetries }}",
		"{{ nginx.image }}":                   "{{ .Nginx.Image }}",
		"{{ nginx.port }}":                    "{{ .Nginx.Port }}",
		"{{ nginx.proxy_timeout }}":           "{{ .Nginx.ProxyTimeout }}",
		"{{ nginx.access_log }}":              "{{ .Nginx.AccessLog }}",
		"{{ network.name }}":                  "{{ .Network.Name }}",
		"{{ network.driver_type }}":           "{{ .Network.DriverType }}",
		"{{ opa.image }}":                     "{{ .OPA.Image }}",
		"{{ opa.port }}":                      "{{ .OPA.Port }}",
		"{{ opa.policies_dir }}":              "{{ .OPA.PoliciesDir }}",
	}
	for old, newValue := range replacements {
		content = strings.ReplaceAll(content, old, newValue)
	}
	return content
}

func renderTemplate(tmplFile, outFile string, data any) error {
	tmplPath := filepath.Join(templatesDir, tmplFile)
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("cannot read template %s: %w", tmplFile, err)
	}
	t, err := template.New(tmplFile).Option("missingkey=error").Parse(normalizeTemplate(string(tmplContent)))
	if err != nil {
		return fmt.Errorf("template parse error in %s: %w", tmplFile, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("template execute error: %w", err)
	}
	out := strings.TrimRight(buf.String(), "\r\n") + "\n"
	return os.WriteFile(outFile, []byte(out), 0644)
}

func cmdInit(m *Manifest) {
	hdr("SwiftDeploy Init")
	info("Generating nginx.conf from template...")
	must(renderTemplate("nginx.conf.j2", nginxConfFile, m))
	ok(fmt.Sprintf("Generated: %s", abs(nginxConfFile)))
	info("Generating docker-compose.yml from template...")
	must(renderTemplate("docker-compose.yml.j2", composeFile, m))
	ok(fmt.Sprintf("Generated: %s", abs(composeFile)))
	ok("Init complete. Run ./swiftdeploy validate to verify.")
}

func cmdValidate(m *Manifest) {
	hdr("SwiftDeploy Validate - Pre-flight Checks")
	allPassed := true

	info("Check 1: manifest.yaml exists and is valid YAML")
	if _, err := loadManifest(); err != nil {
		fail(fmt.Sprintf("manifest.yaml invalid: %v", err))
		allPassed = false
	} else {
		ok("manifest.yaml exists and is valid YAML")
	}

	info("Check 2: Required fields present and non-empty")
	missing := requiredFieldFailures(m)
	if len(missing) > 0 {
		fail(fmt.Sprintf("Missing required fields: %s", strings.Join(missing, ", ")))
		allPassed = false
	} else {
		ok("All required fields present and non-empty")
	}

	info("Check 3: Docker image exists locally")
	if result := runCapture("docker", "image", "inspect", m.Services.Image); result.err == nil {
		ok(fmt.Sprintf("Docker image '%s' exists locally", m.Services.Image))
	} else {
		fail(fmt.Sprintf("Docker image '%s' not found locally. Run: docker build -t %s app/", m.Services.Image, m.Services.Image))
		allPassed = false
	}

	info("Check 4: Nginx port is not already bound on host")
	if isPortBound(m.Nginx.Port) {
		fail(fmt.Sprintf("Port %d is already in use on the host", m.Nginx.Port))
		allPassed = false
	} else {
		ok(fmt.Sprintf("Port %d is available", m.Nginx.Port))
	}

	info("Check 5: nginx.conf syntax is valid")
	if !fileExists(nginxConfFile) {
		fail("nginx.conf not found - run ./swiftdeploy init first")
		allPassed = false
	} else {
		mount := fmt.Sprintf("%s:/etc/nginx/nginx.conf:ro", abs(nginxConfFile))
		result := runCapture("docker", "run", "--rm", "--add-host", fmt.Sprintf("%s:127.0.0.1", m.Services.Name), "-v", mount, m.Nginx.Image, "nginx", "-t")
		if result.err == nil {
			ok("nginx.conf syntax is valid")
		} else {
			fail(fmt.Sprintf("nginx.conf syntax error:\n%s", result.stderr))
			allPassed = false
		}
	}

	fmt.Println()
	if allPassed {
		ok("All 5 checks passed. Ready to deploy.")
		return
	}
	fail("One or more checks failed. Fix issues before deploying.")
	os.Exit(1)
}

func requiredFieldFailures(m *Manifest) []string {
	fields := []struct {
		name string
		bad  bool
	}{
		{"services.name", strings.TrimSpace(m.Services.Name) == ""},
		{"services.image", strings.TrimSpace(m.Services.Image) == ""},
		{"services.port", m.Services.Port == 0},
		{"services.mode", strings.TrimSpace(m.Services.Mode) == ""},
		{"services.version", strings.TrimSpace(m.Services.Version) == ""},
		{"services.restart_policy", strings.TrimSpace(m.Services.RestartPolicy) == ""},
		{"services.log_volume", strings.TrimSpace(m.Services.LogVolume) == ""},
		{"services.healthcheck_path", strings.TrimSpace(m.Services.HealthcheckPath) == ""},
		{"services.healthcheck_interval", strings.TrimSpace(m.Services.HealthcheckInterval) == ""},
		{"services.healthcheck_timeout", strings.TrimSpace(m.Services.HealthcheckTimeout) == ""},
		{"services.healthcheck_retries", m.Services.HealthcheckRetries == 0},
		{"nginx.image", strings.TrimSpace(m.Nginx.Image) == ""},
		{"nginx.port", m.Nginx.Port == 0},
		{"nginx.proxy_timeout", strings.TrimSpace(m.Nginx.ProxyTimeout) == ""},
		{"nginx.access_log", strings.TrimSpace(m.Nginx.AccessLog) == ""},
		{"network.name", strings.TrimSpace(m.Network.Name) == ""},
		{"network.driver_type", strings.TrimSpace(m.Network.DriverType) == ""},
		{"opa.image", strings.TrimSpace(m.OPA.Image) == ""},
		{"opa.port", m.OPA.Port == 0},
		{"opa.policies_dir", strings.TrimSpace(m.OPA.PoliciesDir) == ""},
	}
	var missing []string
	for _, field := range fields {
		if field.bad {
			missing = append(missing, field.name)
		}
	}
	return missing
}

type OPADecision struct {
	Allow       bool     `json:"allow"`
	Violations  []string `json:"violations"`
	Domain      string   `json:"domain"`
	Action      string   `json:"action"`
	EvaluatedAt string   `json:"evaluated_at"`
}

type OPAResponse struct {
	Result OPADecision `json:"result"`
}

func queryOPA(m *Manifest, pkg string, input map[string]any) (*OPADecision, error) {
	opaURL := fmt.Sprintf("http://127.0.0.1:%d/v1/data/%s/decision", m.OPA.Port, strings.ReplaceAll(pkg, ".", "/"))
	payload := map[string]any{"input": input}
	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: opaTimeout}
	resp, err := client.Post(opaURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("OPA unreachable at %s: %w", opaURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("OPA policy not found at package %s - run ./swiftdeploy init", pkg)
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OPA returned HTTP %d for package %s: %s", resp.StatusCode, pkg, strings.TrimSpace(string(data)))
	}
	var opaResp OPAResponse
	if err := json.NewDecoder(resp.Body).Decode(&opaResp); err != nil {
		return nil, fmt.Errorf("OPA response decode failed: %w", err)
	}
	return &opaResp.Result, nil
}

func getHostStats() (diskFreeGB float64, cpuLoad float64, memUsedPct float64) {
	return getDiskFreeGB(), getCPULoad(), getMemUsedPct()
}

func getDiskFreeGB() float64 {
	out, err := exec.Command("df", "-BG", "/").Output()
	if err != nil {
		return 999.0
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return 999.0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 999.0
	}
	val := strings.TrimSuffix(fields[3], "G")
	gb, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 999.0
	}
	return gb
}

func getCPULoad() float64 {
	if runtime.GOOS != "linux" {
		return 0.0
	}
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0.0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0.0
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0.0
	}
	return load
}

func getMemUsedPct() float64 {
	if runtime.GOOS != "linux" {
		return 0.0
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0.0
	}
	values := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			key := strings.TrimSuffix(parts[0], ":")
			val, err := strconv.ParseFloat(parts[1], 64)
			if err == nil {
				values[key] = val
			}
		}
	}
	total := values["MemTotal"]
	avail := values["MemAvailable"]
	if total == 0 {
		return 0.0
	}
	return ((total - avail) / total) * 100
}

type ScrapedMetrics struct {
	TotalRequests int64
	ErrorRequests int64
	ErrorRatePct  float64
	P99LatencyMs  float64
	ReqPerSec     float64
	ChaosActive   int
	Mode          string
	UptimeSeconds float64
	ScrapedAt     time.Time
}

func scrapeMetrics(m *Manifest) (*ScrapedMetrics, error) {
	url := fmt.Sprintf("http://localhost:%d/metrics", m.Nginx.Port)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot reach /metrics at %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/metrics returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parsePrometheusMetrics(string(body)), nil
}

func parsePrometheusMetrics(raw string) *ScrapedMetrics {
	sm := &ScrapedMetrics{ScrapedAt: time.Now()}
	reReq := regexp.MustCompile(`http_requests_total\{[^}]*status_code="(\d+)"[^}]*\}\s+(\d+)`)
	for _, match := range reReq.FindAllStringSubmatch(raw, -1) {
		count, _ := strconv.ParseInt(match[2], 10, 64)
		sm.TotalRequests += count
		code, _ := strconv.Atoi(match[1])
		if code >= 400 {
			sm.ErrorRequests += count
		}
	}
	if sm.TotalRequests > 0 {
		sm.ErrorRatePct = float64(sm.ErrorRequests) / float64(sm.TotalRequests) * 100
	}

	reRecentReq := regexp.MustCompile(`swiftdeploy_recent_http_requests_total\{[^}]*status_code="(\d+)"[^}]*\}\s+(\d+)`)
	var recentTotal, recentErrors int64
	for _, match := range reRecentReq.FindAllStringSubmatch(raw, -1) {
		count, _ := strconv.ParseInt(match[2], 10, 64)
		recentTotal += count
		code, _ := strconv.Atoi(match[1])
		if code >= 400 {
			recentErrors += count
		}
	}
	if recentTotal > 0 {
		sm.ErrorRatePct = float64(recentErrors) / float64(recentTotal) * 100
	}

	if p99, ok := parseHistogramP99(raw, "swiftdeploy_recent_request_duration_seconds_bucket"); ok {
		sm.P99LatencyMs = p99
	} else if p99, ok := parseHistogramP99(raw, "http_request_duration_seconds_bucket"); ok {
		sm.P99LatencyMs = p99
	}
	if math.IsInf(sm.P99LatencyMs, 1) {
		sm.P99LatencyMs = 10000
	}

	reUptime := regexp.MustCompile(`app_uptime_seconds\s+([\d.]+)`)
	if m := reUptime.FindStringSubmatch(raw); len(m) > 1 {
		sm.UptimeSeconds, _ = strconv.ParseFloat(m[1], 64)
	}
	reMode := regexp.MustCompile(`app_mode\{mode="([^"]+)"\}\s+(\d+)`)
	if m := reMode.FindStringSubmatch(raw); len(m) > 1 {
		sm.Mode = m[1]
	}
	reChaos := regexp.MustCompile(`chaos_active\s+(\d+)`)
	if m := reChaos.FindStringSubmatch(raw); len(m) > 1 {
		sm.ChaosActive, _ = strconv.Atoi(m[1])
	}
	return sm
}

func parseHistogramP99(raw, metricName string) (float64, bool) {
	reBucket := regexp.MustCompile(regexp.QuoteMeta(metricName) + `\{le="([^"]+)"[^}]*\}\s+(\d+)`)
	type bucket struct{ le, count float64 }
	aggregateBuckets := map[float64]float64{}
	for _, match := range reBucket.FindAllStringSubmatch(raw, -1) {
		count, _ := strconv.ParseFloat(match[2], 64)
		var leVal float64
		if match[1] == "+Inf" {
			leVal = math.Inf(1)
		} else {
			leVal, _ = strconv.ParseFloat(match[1], 64)
		}
		aggregateBuckets[leVal] += count
	}
	if len(aggregateBuckets) == 0 {
		return 0, false
	}
	var buckets []bucket
	for le, count := range aggregateBuckets {
		buckets = append(buckets, bucket{le, count})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].le < buckets[j].le })
	total := buckets[len(buckets)-1].count
	if total == 0 {
		return 0, true
	}
	target := math.Ceil(total * 0.99)
	for _, b := range buckets {
		if b.count >= target {
			return b.le * 1000, true
		}
	}
	return 0, true
}

type HistoryEntry struct {
	Timestamp    string  `json:"timestamp"`
	Mode         string  `json:"mode"`
	ReqPerSec    float64 `json:"req_per_sec"`
	ErrorRatePct float64 `json:"error_rate_pct"`
	P99LatencyMs float64 `json:"p99_latency_ms"`
	ChaosActive  int     `json:"chaos_active"`
	Event        string  `json:"event,omitempty"`
	PolicyResult string  `json:"policy_result,omitempty"`
}

func appendHistory(entry HistoryEntry) {
	f, err := os.OpenFile(historyFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		warn(fmt.Sprintf("Cannot write history: %v", err))
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	_, _ = f.Write(append(line, '\n'))
}

func preDeployCheck(m *Manifest) bool {
	hdr("Pre-Deploy Policy Check")
	diskFree, cpuLoad, memUsed := getHostStats()
	info(fmt.Sprintf("Host stats: disk_free=%.2fGB cpu_load=%.2f mem_used=%.2f%%", diskFree, cpuLoad, memUsed))
	input := map[string]any{
		"disk_free_gb":     diskFree,
		"cpu_load":         cpuLoad,
		"mem_used_percent": memUsed,
		"action":           "deploy",
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
	}
	decision, err := queryOPA(m, "swiftdeploy.infrastructure", input)
	if err != nil {
		fail(fmt.Sprintf("Deploy BLOCKED: infrastructure policy decision unavailable: %v", err))
		appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Event: "pre-deploy-opa-unavailable", PolicyResult: err.Error()})
		return false
	}
	appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Event: "pre-deploy-policy-check", PolicyResult: fmt.Sprintf("allow=%v violations=%v", decision.Allow, decision.Violations)})
	if !decision.Allow {
		fail(fmt.Sprintf("Deploy BLOCKED by policy [domain: %s]", decision.Domain))
		for _, v := range decision.Violations {
			fail(fmt.Sprintf("  Violation: %s", v))
		}
		return false
	}
	ok(fmt.Sprintf("Infrastructure policy passed [domain: %s]", decision.Domain))
	return true
}

func prePromoteCheck(m *Manifest) bool {
	hdr("Pre-Promote Policy Check")
	sm, err := scrapeMetrics(m)
	if err != nil {
		fail(fmt.Sprintf("Promote BLOCKED: cannot scrape /metrics for canary safety input: %v", err))
		appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Event: "pre-promote-metrics-unavailable", PolicyResult: err.Error()})
		return false
	}
	info(fmt.Sprintf("Canary metrics: error_rate=%.2f%% p99_latency=%.2fms", sm.ErrorRatePct, sm.P99LatencyMs))
	input := map[string]any{
		"error_rate_percent": sm.ErrorRatePct,
		"p99_latency_ms":     sm.P99LatencyMs,
		"action":             "promote",
		"target_mode":        "stable",
		"timestamp":          time.Now().UTC().Format(time.RFC3339),
	}
	decision, err := queryOPA(m, "swiftdeploy.canary", input)
	if err != nil {
		fail(fmt.Sprintf("Promote BLOCKED: canary policy decision unavailable: %v", err))
		appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Event: "pre-promote-opa-unavailable", PolicyResult: err.Error()})
		return false
	}
	appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Mode: sm.Mode, ErrorRatePct: sm.ErrorRatePct, P99LatencyMs: sm.P99LatencyMs, Event: "pre-promote-policy-check", PolicyResult: fmt.Sprintf("allow=%v violations=%v", decision.Allow, decision.Violations)})
	if !decision.Allow {
		fail(fmt.Sprintf("Promote BLOCKED by policy [domain: %s]", decision.Domain))
		for _, v := range decision.Violations {
			fail(fmt.Sprintf("  Violation: %s", v))
		}
		return false
	}
	ok(fmt.Sprintf("Canary safety policy passed [domain: %s]", decision.Domain))
	return true
}

func cmdDeploy(m *Manifest) {
	hdr("SwiftDeploy Deploy")
	cmdInit(m)
	if !ensureOPAReady(m) {
		os.Exit(1)
	}
	if !preDeployCheck(m) {
		os.Exit(1)
	}
	info("Bringing up the stack...")
	if err := runStreaming("docker", "compose", "-f", composeFile, "up", "-d", "--remove-orphans"); err != nil {
		fail("docker compose up failed")
		os.Exit(1)
	}
	healthURL := fmt.Sprintf("http://localhost:%d%s", m.Nginx.Port, m.Services.HealthcheckPath)
	info("Waiting for health checks to pass (timeout: 60s)...")
	info(fmt.Sprintf("Health URL: %s", healthURL))
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, err := httpGet(healthURL, 3*time.Second); err == nil && status == http.StatusOK {
			ok(fmt.Sprintf("Stack is healthy and responding at http://localhost:%d", m.Nginx.Port))
			ok("Deploy complete.")
			return
		}
		time.Sleep(3 * time.Second)
		fmt.Print(".")
	}
	fmt.Println()
	fail("Health checks did not pass within 60 seconds.")
	os.Exit(1)
}

func ensureOPAReady(m *Manifest) bool {
	hdr("SwiftDeploy OPA Bootstrap")
	info("Starting OPA policy sidecar...")
	if err := runStreaming("docker", "compose", "-f", composeFile, "up", "-d", "opa"); err != nil {
		fail(fmt.Sprintf("OPA bootstrap failed: %v", err))
		return false
	}
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", m.OPA.Port)
	info(fmt.Sprintf("Waiting for OPA health at %s...", healthURL))
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if status, _, err := httpGet(healthURL, 3*time.Second); err == nil && status == http.StatusOK {
			ok("OPA is healthy and ready for policy decisions")
			return true
		}
		time.Sleep(2 * time.Second)
		fmt.Print(".")
	}
	fmt.Println()
	fail("OPA did not become healthy within 45 seconds")
	return false
}

func cmdPromote(m *Manifest, targetMode string) {
	if targetMode != "canary" && targetMode != "stable" {
		fail(fmt.Sprintf("Invalid mode '%s'. Must be: canary or stable", targetMode))
		os.Exit(1)
	}
	hdr(fmt.Sprintf("SwiftDeploy Promote -> %s", targetMode))
	if m.Services.Mode == targetMode {
		warn(fmt.Sprintf("Already in %s mode. No action taken.", targetMode))
		return
	}
	info(fmt.Sprintf("Switching from %s -> %s", m.Services.Mode, targetMode))
	manifestText, err := os.ReadFile(manifestFile)
	must(err)
	modeLine := regexp.MustCompile(`(?m)^(\s*mode:\s*)\S+`)
	updated := modeLine.ReplaceAllString(string(manifestText), "${1}"+targetMode)
	if updated == string(manifestText) {
		fail("could not find services.mode in manifest.yaml")
		os.Exit(1)
	}
	must(os.WriteFile(manifestFile, []byte(updated), 0644))
	ok(fmt.Sprintf("manifest.yaml updated: mode = %s", targetMode))
	updatedManifest, err := loadManifest()
	must(err)
	must(renderTemplate("docker-compose.yml.j2", composeFile, updatedManifest))
	ok("docker-compose.yml regenerated")
	info(fmt.Sprintf("Restarting service container: %s...", updatedManifest.Services.Name))
	if err := runStreaming("docker", "compose", "-f", composeFile, "up", "-d", "--no-deps", "--force-recreate", updatedManifest.Services.Name); err != nil {
		fail("Service restart failed")
		os.Exit(1)
	}
	info("Waiting for service to become healthy...")
	healthURL := fmt.Sprintf("http://localhost:%d%s", updatedManifest.Nginx.Port, updatedManifest.Services.HealthcheckPath)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status, body, err := httpGet(healthURL, 3*time.Second)
		if err == nil && status == http.StatusOK {
			var payload map[string]any
			if json.Unmarshal(body, &payload) == nil && payload["mode"] == targetMode {
				ok(fmt.Sprintf("Mode confirmed via /healthz: mode=%s", targetMode))
				ok(fmt.Sprintf("Promote to %s complete.", targetMode))
				appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Mode: targetMode, Event: "promote-" + targetMode})
				return
			}
		}
		time.Sleep(3 * time.Second)
		fmt.Print(".")
	}
	fmt.Println()
	fail(fmt.Sprintf("Could not confirm %s mode via /healthz within 60s", targetMode))
	os.Exit(1)
}

func cmdTeardown(_ *Manifest, args []string) {
	hdr("SwiftDeploy Teardown")
	clean := contains(args, "--clean")
	info("Stopping and removing containers, networks, volumes...")
	if fileExists(composeFile) {
		if err := runStreaming("docker", "compose", "-f", composeFile, "down", "-v", "--remove-orphans"); err != nil {
			fail("docker compose down failed")
			os.Exit(1)
		}
	} else {
		warn("docker-compose.yml not found; skipping docker compose down")
	}
	ok("Containers, networks, and volumes removed")
	if clean {
		info("--clean flag set: removing generated config files...")
		for _, path := range []string{nginxConfFile, composeFile} {
			if fileExists(path) {
				must(os.Remove(path))
				ok(fmt.Sprintf("Deleted: %s", abs(path)))
			}
		}
		ok("Clean teardown complete.")
		return
	}
	ok("Teardown complete. Generated configs preserved.")
}

func cmdStatus(m *Manifest) {
	hdr("SwiftDeploy Status - Live Dashboard")
	info("Refreshing every 3 seconds. Press Ctrl+C to exit.\n")
	var prevTotal int64
	var prevTime time.Time
	for {
		sm, err := scrapeMetrics(m)
		if err != nil {
			warn(fmt.Sprintf("Metrics scrape failed: %v", err))
			time.Sleep(3 * time.Second)
			continue
		}
		if !prevTime.IsZero() {
			elapsed := sm.ScrapedAt.Sub(prevTime).Seconds()
			if elapsed > 0 {
				sm.ReqPerSec = float64(sm.TotalRequests-prevTotal) / elapsed
			}
		}
		prevTotal = sm.TotalRequests
		prevTime = sm.ScrapedAt
		infraDecision, infraErr := queryOPA(m, "swiftdeploy.infrastructure", map[string]any{"disk_free_gb": getDiskFreeGB(), "cpu_load": getCPULoad(), "action": "status", "timestamp": time.Now().UTC().Format(time.RFC3339)})
		canaryDecision, canaryErr := queryOPA(m, "swiftdeploy.canary", map[string]any{"error_rate_percent": sm.ErrorRatePct, "p99_latency_ms": sm.P99LatencyMs, "action": "status", "target_mode": m.Services.Mode, "timestamp": time.Now().UTC().Format(time.RFC3339)})
		fmt.Print("\033[H\033[2J")
		fmt.Printf("%sSwiftDeploy Live Status Dashboard%s\n\n", colorBold, colorReset)
		fmt.Printf("Mode:           %s\n", sm.Mode)
		fmt.Printf("Req/s:          %.2f\n", sm.ReqPerSec)
		fmt.Printf("P99 Latency:    %.2fms\n", sm.P99LatencyMs)
		fmt.Printf("Error Rate:     %.2f%%\n", sm.ErrorRatePct)
		fmt.Printf("Total Requests: %d\n", sm.TotalRequests)
		fmt.Printf("Uptime:         %.0fs\n", sm.UptimeSeconds)
		chaosLabel := []string{"none", "slow", "error"}
		chaosIdx := sm.ChaosActive
		if chaosIdx < 0 || chaosIdx >= len(chaosLabel) {
			chaosIdx = 0
		}
		fmt.Printf("Chaos:          %s\n", chaosLabel[chaosIdx])
		fmt.Printf("\n%sPolicy Compliance%s\n", colorBold, colorReset)
		printPolicyStatus("infrastructure", infraDecision, infraErr)
		printPolicyStatus("canary", canaryDecision, canaryErr)
		policyResult := "passing"
		if (infraDecision != nil && !infraDecision.Allow) || (canaryDecision != nil && !canaryDecision.Allow) {
			policyResult = "failing"
		}
		appendHistory(HistoryEntry{Timestamp: time.Now().UTC().Format(time.RFC3339), Mode: sm.Mode, ReqPerSec: sm.ReqPerSec, ErrorRatePct: sm.ErrorRatePct, P99LatencyMs: sm.P99LatencyMs, ChaosActive: sm.ChaosActive, Event: "status-scrape", PolicyResult: policyResult})
		fmt.Printf("\n%sLast updated: %s%s\n\n", colorBlue, time.Now().Format("15:04:05"), colorReset)
		time.Sleep(3 * time.Second)
	}
}

func printPolicyStatus(domain string, decision *OPADecision, err error) {
	if err != nil {
		fmt.Printf("  %s[%s] OPA unavailable: %v%s\n", colorYellow, domain, err, colorReset)
		return
	}
	if decision.Allow {
		fmt.Printf("  %s[%s] PASSING%s\n", colorGreen, domain, colorReset)
		return
	}
	fmt.Printf("  %s[%s] FAILING%s\n", colorRed, domain, colorReset)
	for _, violation := range decision.Violations {
		fmt.Printf("    %s- %s%s\n", colorRed, violation, colorReset)
	}
}

func cmdAudit() {
	hdr("SwiftDeploy Audit - Generating audit_report.md")
	f, err := os.Open(historyFile)
	if err != nil {
		fail(fmt.Sprintf("Cannot open %s: %v", historyFile, err))
		fail("Run ./swiftdeploy status first to generate history data")
		os.Exit(1)
	}
	defer f.Close()
	var entries []HistoryEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			entries = append(entries, e)
		}
	}
	var sb strings.Builder
	sb.WriteString("# SwiftDeploy Audit Report\n\n")
	sb.WriteString(fmt.Sprintf("**Generated:** %s\n\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("**Total Events:** %d\n\n", len(entries)))
	sb.WriteString("## Timeline\n\n")
	sb.WriteString("| Timestamp | Event | Mode | Req/s | Error Rate | P99 Latency | Chaos |\n")
	sb.WriteString("|-----------|-------|------|-------|------------|-------------|-------|\n")
	chaosLabels := []string{"none", "slow", "error"}
	for _, e := range entries {
		chaosLabel := "none"
		if e.ChaosActive >= 0 && e.ChaosActive < len(chaosLabels) {
			chaosLabel = chaosLabels[e.ChaosActive]
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f | %.2f%% | %.2fms | %s |\n", e.Timestamp, e.Event, e.Mode, e.ReqPerSec, e.ErrorRatePct, e.P99LatencyMs, chaosLabel))
	}
	sb.WriteString("\n## Policy Violations\n\n")
	var violations []HistoryEntry
	for _, e := range entries {
		if strings.Contains(e.PolicyResult, "allow=false") || e.PolicyResult == "failing" {
			violations = append(violations, e)
		}
	}
	if len(violations) == 0 {
		sb.WriteString("No policy violations recorded.\n")
	} else {
		sb.WriteString("| Timestamp | Event | Policy Result |\n")
		sb.WriteString("|-----------|-------|---------------|\n")
		for _, e := range violations {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", e.Timestamp, e.Event, e.PolicyResult))
		}
	}
	sb.WriteString("\n## Mode Changes\n\n")
	prevMode := ""
	wroteMode := false
	for _, e := range entries {
		if e.Mode != "" && e.Mode != prevMode {
			if !wroteMode {
				sb.WriteString("| Timestamp | Mode | Event |\n|-----------|------|-------|\n")
				wroteMode = true
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", e.Timestamp, e.Mode, e.Event))
			prevMode = e.Mode
		}
	}
	if !wroteMode {
		sb.WriteString("No mode changes recorded.\n")
	}
	sb.WriteString("\n## Chaos Events\n\n")
	prevChaos := 0
	wroteChaos := false
	for _, e := range entries {
		if e.ChaosActive != prevChaos {
			if !wroteChaos {
				sb.WriteString("| Timestamp | Chaos State | Event |\n|-----------|-------------|-------|\n")
				wroteChaos = true
			}
			chaosLabel := "none"
			if e.ChaosActive >= 0 && e.ChaosActive < len(chaosLabels) {
				chaosLabel = chaosLabels[e.ChaosActive]
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", e.Timestamp, chaosLabel, e.Event))
			prevChaos = e.ChaosActive
		}
	}
	if !wroteChaos {
		sb.WriteString("No chaos state changes recorded.\n")
	}
	must(os.WriteFile(auditReportFile, []byte(sb.String()), 0644))
	ok(fmt.Sprintf("Audit report written to %s", auditReportFile))
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	m, err := loadManifest()
	if err != nil {
		fail(err.Error())
		os.Exit(1)
	}
	switch os.Args[1] {
	case "init":
		cmdInit(m)
	case "validate":
		cmdValidate(m)
	case "deploy":
		cmdDeploy(m)
	case "promote":
		if len(os.Args) < 3 {
			fail("Usage: ./swiftdeploy promote [canary|stable]")
			os.Exit(1)
		}
		targetMode := os.Args[2]
		if targetMode == "stable" && m.Services.Mode == "canary" && !prePromoteCheck(m) {
			os.Exit(1)
		}
		cmdPromote(m, targetMode)
	case "teardown":
		cmdTeardown(m, os.Args[2:])
	case "status":
		cmdStatus(m)
	case "audit":
		cmdAudit()
	default:
		fail(fmt.Sprintf("Unknown subcommand: %s", os.Args[1]))
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`
%sSwiftDeploy CLI%s
Usage: ./swiftdeploy <subcommand> [options]

Subcommands:
  init                 Generate nginx.conf + docker-compose.yml from manifest.yaml
  validate             Run 5 pre-flight checks
  deploy               OPA pre-deploy check + init + bring up stack
  promote [mode]       OPA pre-promote check + switch canary/stable
  teardown [--clean]   Remove containers/networks/volumes
  status               Live metrics dashboard + policy compliance
  audit                Generate audit_report.md from history.jsonl
`, colorBold, colorReset)
}

type commandResult struct {
	stdout string
	stderr string
	err    error
}

func runCapture(name string, args ...string) commandResult {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func runStreaming(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func httpGet(url string, timeout time.Duration) (int, []byte, error) {
	client := http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, readErr
	}
	return resp.StatusCode, body, nil
}

func isPortBound(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func abs(path string) string {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return resolved
}

func must(err error) {
	if err != nil {
		fail(err.Error())
		os.Exit(1)
	}
}
