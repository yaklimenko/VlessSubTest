package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The /probe endpoint implements a long-running "doomscrolling" load test:
//
//  1. The subscription is fetched and parsed (same code path as /test).
//  2. For every key (up to probeMaxParallel at once, sequential per key):
//     a. the proxy engine (sing-box, or xray for xhttp keys) is started on a
//     freshly allocated SOCKS5 port via startProxyEngine (shared with /test);
//     b. a quick connectivity check downloads the probe file through the proxy
//     (10 s cap). If it fails, the key is marked FAILED and skipped;
//     c. repeated rate-limited download sessions of the probe file are run
//     ("doomscroll"): each session is a separate curl capped at
//     --limit-rate <target_kbps*128> bytes for ~probeSessionTimeoutSec seconds,
//     followed by a random 2-5 s pause, until duration_sec elapses.
//  3. Metrics are accumulated per key and mapped to a verdict:
//     OK       if avg_speed >= 0.8*target AND stability >= 80%
//     DEGRADED if avg_speed >= 0.5*target OR  stability >= 50%
//     FAILED   otherwise
//
// NOTE on units: target_kbps is interpreted as *kilobits per second*
// (binary: 1 Kb = 1024 bits = 128 bytes). curl's --limit-rate works in
// bytes, so it is fed "<target_kbps*128>" bytes (= "<target_kbps/8>K").
// avg_speed_kbps is reported in the same units (bytes/s / 128), so a
// fully working key at target=4000 measures ~4000 and the OK threshold
// (0.8*target) distinguishes real throughput degradation.
// The units decision follows PROBE_REPORT.md ("Требуется решение Антона").

const (
	// Defaults for the /probe request.
	probeDefaultDurationSec = 180
	probeMaxDurationSec     = 900 // hard cap: 15 min per probe (endpoint guard)
	probeDefaultTargetKbps  = 4000
	probeDefaultProbeURL    = "https://203.0.113.5/speedtest/tcb.mp4" // private video resource on Stockholm VPS (nginx, LE cert)

	// Parallelism: keys are tested sequentially, at most 2 at a time.
	probeMinParallel = 1
	probeMaxParallel = 2

	// Timing.
	probeConnectTimeoutSec  = 10 // connectivity check cap
	probeSessionTimeoutSec  = 10 // one download session ("~10 s chunk"; ~5 MB at target rate)
	probeEngineStartTimeout = 15 // wait for the SOCKS5 listener after engine start
	probePauseMinSec        = 2  // pause between sessions (random 2-5 s)
	probePauseMaxSec        = 5
	probeEngineMarginSec    = 60 // engine context outlives the loop by a margin

	// Verdict thresholds.
	verdictOKSpeedFrac     = 0.8  // OK requires avg_speed >= 0.8 * target
	verdictOKStabilityPct  = 80.0 // OK requires stability >= 80%
	verdictDegSpeedFrac    = 0.5  // DEGRADED requires avg_speed >= 0.5 * target
	verdictDegStabilityPct = 50.0 // ... or stability >= 50%
)

// ProbeRequest is the JSON body of POST /probe.
type ProbeRequest struct {
	URL         string `json:"url"`
	ProbeURL    string `json:"probe_url"`
	DurationSec int    `json:"duration_sec"`
	TargetKbps  int    `json:"target_kbps"`
	Parallel    int    `json:"parallel"`
}

// ProbeKeyResult is the per-key outcome of a /probe run.
type ProbeKeyResult struct {
	KeyIdx            int     `json:"key_idx"`
	Remark            string  `json:"remark,omitempty"`
	IP                string  `json:"ip,omitempty"`
	Status            string  `json:"status"` // OK | DEGRADED | FAILED
	AvgSpeedKbps      float64 `json:"avg_speed_kbps"`
	StabilityPct      float64 `json:"stability_pct"`
	Reconnects        int     `json:"reconnects"`
	LatencyMs         float64 `json:"latency_ms"`
	TotalDownloadedMB float64 `json:"total_downloaded_mb"`
	SessionsOK        int     `json:"sessions_ok"`
	SessionsFail      int     `json:"sessions_fail"`
	DurationSec       int     `json:"duration_sec"`
	Reason            string  `json:"reason,omitempty"`
}

// ProbeResponse is the JSON response of POST /probe.
type ProbeResponse struct {
	Total    int              `json:"total"`
	OK       int              `json:"ok"`
	Degraded int              `json:"degraded"`
	Failed   int              `json:"failed"`
	Results  []ProbeKeyResult `json:"results"`
}

// applyProbeDefaults fills in default values for a ProbeRequest.
func applyProbeDefaults(req *ProbeRequest) {
	if req.ProbeURL == "" {
		req.ProbeURL = probeDefaultProbeURL
	}
	if req.DurationSec <= 0 {
		req.DurationSec = probeDefaultDurationSec
	}
	if req.DurationSec > probeMaxDurationSec {
		req.DurationSec = probeMaxDurationSec
	}
	if req.TargetKbps <= 0 {
		req.TargetKbps = probeDefaultTargetKbps
	}
	if req.Parallel < probeMinParallel {
		req.Parallel = probeMinParallel
	}
	if req.Parallel > probeMaxParallel {
		req.Parallel = probeMaxParallel
	}
}

// handleProbe serves POST /probe. If a run store is configured (--log), the
// result (success or failure) is persisted for the panel to fetch later.
func handleProbe(w http.ResponseWriter, r *http.Request, store *RunStore, singBoxPath, xrayPath string, verbose, keepLogs bool) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req ProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, `{"error":"url is required"}`, http.StatusBadRequest)
		return
	}

	applyProbeDefaults(&req)

	startedAt := time.Now()
	// r.Context() propagates client disconnects so a cancelled request aborts
	// the probe instead of burning resources for the full duration.
	resp, err := runProbeSubscription(r.Context(), req, singBoxPath, xrayPath, verbose, keepLogs)

	if store != nil {
		if serr := store.SaveRun(buildProbeRunRecord(req, startedAt, time.Now(), resp, err)); serr != nil {
			fmt.Fprintf(os.Stderr, "probe: failed to save run: %v\n", serr)
		}
	}

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// runProbeSubscription is the shared probe pipeline (fetch → parse → run →
// merge → summarize). It is used both by POST /probe and by the cron
// scheduler; the caller supplies the context (request ctx for HTTP, a timeout
// ctx for cron). req must already have defaults applied.
func runProbeSubscription(ctx context.Context, req ProbeRequest, singBoxPath, xrayPath string, verbose, keepLogs bool) (ProbeResponse, error) {
	fmt.Fprintf(os.Stderr, "Fetching subscription from %s ...\n", req.URL)

	subscriptionLines, err := FetchSubscription(req.URL)
	if err != nil {
		return ProbeResponse{}, err
	}

	if len(subscriptionLines) == 0 {
		return ProbeResponse{}, fmt.Errorf("empty subscription")
	}

	var keys []*VlessKey
	var preResults []ProbeKeyResult

	for i, line := range subscriptionLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, err := ParseVlessURI(line)
		if err != nil {
			preResults = append(preResults, ProbeKeyResult{
				KeyIdx: i,
				Status: "FAILED",
				Reason: "parse_error",
			})
			continue
		}
		keys = append(keys, key)
	}

	if len(keys) == 0 && len(preResults) == 0 {
		return ProbeResponse{}, fmt.Errorf("no valid vless keys found")
	}

	fmt.Fprintf(os.Stderr, "Probing %d vless keys (duration=%ds target=%dKb/s parallel=%d probe_url=%s)...\n",
		len(keys), req.DurationSec, req.TargetKbps, req.Parallel, req.ProbeURL)

	// Each key is probed sequentially inside its own goroutine; only
	// req.Parallel keys run at the same time.
	results := make([]ProbeKeyResult, len(keys))
	sem := make(chan struct{}, req.Parallel)
	var wg sync.WaitGroup

	for i, key := range keys {
		wg.Add(1)
		go func(idx int, k *VlessKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = probeKey(ctx, idx, k, singBoxPath, xrayPath, req.ProbeURL, req.DurationSec, req.TargetKbps, verbose, keepLogs)
		}(i, key)
	}

	wg.Wait()

	// Merge parse failures back into the results in subscription-line order
	// (mirrors the /test handler).
	finalResults := make([]ProbeKeyResult, 0, len(results)+len(preResults))
	keyIdx := 0
	preIdx := 0

	for i, line := range subscriptionLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if preIdx < len(preResults) && preResults[preIdx].KeyIdx == i {
			finalResults = append(finalResults, preResults[preIdx])
			preIdx++
		} else if keyIdx < len(results) {
			finalResults = append(finalResults, results[keyIdx])
			keyIdx++
		}
	}

	resp := ProbeResponse{Results: finalResults}
	resp.Total = len(finalResults)
	for _, rr := range finalResults {
		switch rr.Status {
		case "OK":
			resp.OK++
		case "DEGRADED":
			resp.Degraded++
		default:
			resp.Failed++
		}
	}

	return resp, nil
}

// probeKey runs the full long-duration probe for a single key and returns its
// verdict and metrics. It owns a proxy port for the whole duration. If the
// parent ctx (client request) is cancelled, the engine is killed and the loop
// stops.
func probeKey(ctx context.Context, idx int, key *VlessKey, singBoxPath, xrayPath, probeURL string, durationSec, targetKbps int, verbose, keepLogs bool) ProbeKeyResult {
	res := ProbeKeyResult{
		KeyIdx:      idx,
		Remark:      key.Remark,
		IP:          key.Address,
		Status:      "FAILED",
		DurationSec: durationSec,
	}

	port, err := allocatePort()
	if err != nil {
		res.Reason = fmt.Sprintf("NO_PORT: %v", err)
		return res
	}
	defer releasePort(port)

	// The engine context outlives the loop: it is cancelled (and the process
	// killed by exec.CommandContext) even if the loop is exited early. It is
	// derived from the request context so a client disconnect aborts the run.
	engineCtx, engineCancel := context.WithTimeout(ctx, time.Duration(durationSec+probeEngineMarginSec)*time.Second)
	defer engineCancel()

	cleanup, err := startProxyEngine(key, singBoxPath, xrayPath, port, engineCtx, verbose, keepLogs)
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	defer cleanup()

	if !waitForPort(port, probeEngineStartTimeout) {
		res.Reason = "SING_BOX_START_FAILED: port not ready"
		return res
	}

	// Quick connectivity check: download the probe file through the proxy
	// (10 s cap, no rate limit). A key that transfers at least one byte with
	// an HTTP 2xx/3xx passes; anything else is a hard FAILED.
	if ok, reason := probeCheckConnectivity(port, probeURL); !ok {
		res.Reason = reason
		return res
	}

	// "Doomscroll" loop: repeated rate-limited download sessions with random
	// 2-5 s pauses until the deadline.
	start := time.Now()
	deadline := start.Add(time.Duration(durationSec) * time.Second)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	var (
		speedSumBps   float64
		sessionsTotal int
		sessionsOK    int
		sessionsFail  int
		reconnects    int
		stableCount   int
		latencySum    float64
		latencyCount  int
		bytesTotal    int64
	)

	for time.Now().Before(deadline) && engineCtx.Err() == nil {
		speedBps, ttfSec, sizeBytes, httpCode, exitCode, completed := runProbeSession(port, probeURL, targetKbps, probeSessionTimeoutSec)

		sessionsTotal++
		speedSumBps += speedBps
		bytesTotal += sizeBytes
		if ttfSec > 0 {
			latencySum += ttfSec
			latencyCount++
		}
		if completed {
			sessionsOK++
		} else {
			sessionsFail++
			reconnects++
			if verbose {
				fmt.Fprintf(os.Stderr, "probe: key %d session failed (exit=%d http=%d)\n", idx, exitCode, httpCode)
			}
		}
		if speedBps/128 >= verdictOKSpeedFrac*float64(targetKbps) {
			stableCount++
		}

		// Pause 2-5 s between sessions, unless the deadline is near.
		pause := time.Duration(probePauseMinSec+rng.Intn(probePauseMaxSec-probePauseMinSec+1)) * time.Second
		if time.Now().Add(pause).After(deadline) {
			break
		}
		time.Sleep(pause)
	}

	elapsed := int(time.Since(start).Seconds())
	res.DurationSec = elapsed

	// Metrics. Failed sessions contribute their (partial) measured speed to
	// the average and 0 speed to the stability count; guards against
	// division by zero when no session completed.
	if sessionsTotal > 0 {
		res.AvgSpeedKbps = speedSumBps / 128 / float64(sessionsTotal)
		res.StabilityPct = 100 * float64(stableCount) / float64(sessionsTotal)
	}
	if latencyCount > 0 {
		res.LatencyMs = latencySum / float64(latencyCount) * 1000
	}
	res.Reconnects = reconnects
	res.SessionsOK = sessionsOK
	res.SessionsFail = sessionsFail
	res.TotalDownloadedMB = float64(bytesTotal) / 1024 / 1024

	switch probeVerdict(res.AvgSpeedKbps, res.StabilityPct, targetKbps) {
	case "OK":
		res.Status = "OK"
	case "DEGRADED":
		res.Status = "DEGRADED"
	default:
		res.Status = "FAILED"
		if sessionsTotal > 0 && sessionsOK == 0 {
			res.Reason = "ALL_SESSIONS_FAILED"
		} else {
			res.Reason = "VERDICT_FAILED"
		}
	}

	return res
}

// runProbeSession executes one rate-limited download session through the
// SOCKS5 proxy and returns the measured metrics. Exit code 28 (--max-time
// hit) is the normal "chunk end"; any other non-zero exit, or a non-2xx/3xx
// HTTP status, means the session failed (counted as a reconnect).
func runProbeSession(port int, probeURL string, targetKbps, sessionTimeoutSec int) (speedBps, ttfSec float64, sizeBytes int64, httpCode, exitCode int, completed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sessionTimeoutSec+5)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "curl",
		"-x", fmt.Sprintf("socks5h://127.0.0.1:%d", port),
		"-sS",
		"-o", "/dev/null",
		"--limit-rate", strconv.Itoa(targetKbps*128), // targetKbps in kilobits, curl --limit-rate in bytes (see header comment)
		"--connect-timeout", "5",
		"--max-time", fmt.Sprintf("%d", sessionTimeoutSec),
		"-w", "%{speed_download} %{time_starttransfer} %{http_code} %{size_download}",
		probeURL,
	)

	output, err := cmd.Output()
	exitCode = 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	fields := strings.Fields(string(output))
	if len(fields) >= 4 {
		if s, e := strconv.ParseFloat(fields[0], 64); e == nil {
			speedBps = s
		}
		if t, e := strconv.ParseFloat(fields[1], 64); e == nil {
			ttfSec = t
		}
		if c, e := strconv.Atoi(fields[2]); e == nil {
			httpCode = c
		}
		if b, e := strconv.ParseInt(fields[3], 10, 64); e == nil {
			sizeBytes = b
		}
	}

	completed = (exitCode == 0 || exitCode == 28) && httpCode >= 200 && httpCode < 400
	return
}

// probeCheckConnectivity verifies the key can actually download through the
// proxy. It downloads as much of probeURL as fits into 10 seconds (no rate
// limit) and requires an HTTP 2xx/3xx response with at least one byte, so a
// slow-but-working key still passes. Failure reasons are compact and
// informative (CONNECTION_FAILED, TIMEOUT, HTTP_<code>, CURL_EXIT_<code>).
func probeCheckConnectivity(port int, probeURL string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "curl",
		"-x", fmt.Sprintf("socks5h://127.0.0.1:%d", port),
		"-sS",
		"-o", "/dev/null",
		"--connect-timeout", "5",
		"--max-time", fmt.Sprintf("%d", probeConnectTimeoutSec),
		"-w", "%{http_code} %{size_download}",
		probeURL,
	)

	output, err := cmd.Output()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	fields := strings.Fields(string(output))
	if len(fields) >= 2 {
		code, err1 := strconv.Atoi(fields[0])
		size, err2 := strconv.ParseInt(fields[1], 10, 64)
		if err1 == nil && err2 == nil && code >= 200 && code < 400 && size > 0 {
			return true, ""
		}
	}

	// Classify the failure into a compact reason.
	switch {
	case ctx.Err() != nil || exitCode == 28:
		return false, "TIMEOUT"
	case exitCode == 7 || exitCode == 6 || exitCode == 97 || exitCode == 35 || exitCode == 56 || exitCode == 52:
		return false, "CONNECTION_FAILED"
	case len(fields) >= 1:
		if code, err := strconv.Atoi(fields[0]); err == nil && code >= 400 {
			return false, fmt.Sprintf("HTTP_%d", code)
		}
	}
	if exitCode != 0 {
		return false, fmt.Sprintf("CURL_EXIT_%d", exitCode)
	}
	return false, "CONNECTION_FAILED"
}

// probeVerdict maps accumulated metrics to a verdict. Thresholds are
// documented as constants above.
func probeVerdict(avgSpeedKbps, stabilityPct float64, targetKbps int) string {
	target := float64(targetKbps)
	switch {
	case avgSpeedKbps >= verdictOKSpeedFrac*target && stabilityPct >= verdictOKStabilityPct:
		return "OK"
	case avgSpeedKbps >= verdictDegSpeedFrac*target || stabilityPct >= verdictDegStabilityPct:
		return "DEGRADED"
	default:
		return "FAILED"
	}
}
