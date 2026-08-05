package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type TestResult struct {
	KeyIdx          int       `json:"key_idx"`
	Key             *VlessKey `json:"-"`
	Remark          string    `json:"remark"`
	IP              string    `json:"ip"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason,omitempty"`
	YoutubeStatus   string    `json:"-"`
	YoutubeReason   string    `json:"-"`
	InstagramStatus string    `json:"-"`
	InstagramReason string    `json:"-"`
}

func (r TestResult) YoutubeDisplay() string {
	if r.YoutubeStatus == "FAILED" {
		reason := r.YoutubeReason
		if reason == "" {
			reason = "UNKNOWN"
		}
		return fmt.Sprintf("FAILED (%s)", reason)
	}
	return r.YoutubeStatus
}

func (r TestResult) InstagramDisplay() string {
	if r.InstagramStatus == "FAILED" {
		reason := r.InstagramReason
		if reason == "" {
			reason = "UNKNOWN"
		}
		return fmt.Sprintf("FAILED (%s)", reason)
	}
	return r.InstagramStatus
}

const (
	portRangeStart = 10800
	portRangeEnd   = 10900
)

var (
	portMutex sync.Mutex
	usedPorts = map[int]bool{}
	portNext  = portRangeStart
)

func allocatePort() (int, error) {
	portMutex.Lock()
	defer portMutex.Unlock()

	for i := portNext; i <= portRangeEnd; i++ {
		if usedPorts[i] {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", i))
		if err == nil {
			ln.Close()
			usedPorts[i] = true
			portNext = i + 1
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", portRangeStart, portRangeEnd)
}

func releasePort(port int) {
	portMutex.Lock()
	defer portMutex.Unlock()
	delete(usedPorts, port)
	if port < portNext {
		portNext = port
	}
}

func runCurl(port int, targetURL string, timeoutSec int, parentCtx context.Context) (status, reason string) {
	curlCtx, curlCancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer curlCancel()

	curlCmd := exec.CommandContext(curlCtx, "curl",
		"-x", fmt.Sprintf("socks5h://127.0.0.1:%d", port),
		"-sS",
		"-o", "/dev/null",
		"-w", "%{http_code}",
		"--connect-timeout", fmt.Sprintf("%d", timeoutSec/2+2),
		"--max-time", fmt.Sprintf("%d", timeoutSec),
		targetURL,
	)

	output, err := curlCmd.Output()
	exitCode := 0
	var exitErr *exec.ExitError
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitErr = ee
			exitCode = ee.ExitCode()
		}
	}

	if err != nil && exitCode != 0 {
		httpCode := strings.TrimSpace(string(output))
		if httpCode != "" {
			code := strings.TrimSpace(httpCode)
			if parentCtx.Err() != nil || curlCtx.Err() != nil {
				return "FAILED", "TIMEOUT"
			} else if code == "000" {
				return "FAILED", "CONNECTION_FAILED"
			} else {
				return "FAILED", fmt.Sprintf("CURL_EXIT_%d", exitCode)
			}
		}
		if exitErr != nil {
			errStr := err.Error()
			switch {
			case strings.Contains(errStr, "timed out") || strings.Contains(errStr, "timeout"):
				return "FAILED", "TIMEOUT"
			case strings.Contains(errStr, "refused") || strings.Contains(errStr, "reset") || strings.Contains(errStr, "Could not resolve"):
				return "FAILED", "CONNECTION_FAILED"
			default:
				return "FAILED", fmt.Sprintf("CURL_EXIT_%d", exitCode)
			}
		}
	}

	httpCode := strings.TrimSpace(string(output))

	if httpCode == "" || httpCode == "000" {
		return "FAILED", "CONNECTION_FAILED"
	}

	switch {
	case httpCode == "200":
		return "OK", ""
	case httpCode == "301" || httpCode == "302" || httpCode == "307" || httpCode == "308":
		return "OK", ""
	case httpCode[0] == '2':
		return "OK", ""
	default:
		return "FAILED", fmt.Sprintf("HTTP_%s", httpCode)
	}
}

// startProxyEngine starts sing-box (or xray-core for xhttp keys) with a local
// SOCKS5 listener on the given port. The engine runs until ctx is cancelled
// (exec.CommandContext kills the process when the context fires).
//
// On success it returns a cleanup function that kills the process and removes
// temporary files; callers MUST invoke it (typically via defer). On failure it
// removes any written temp files itself and returns the error.
//
// Both the short /test flow (curlTargetURL) and the long /probe flow reuse this
// so the engine lifecycle (start, kill, file cleanup, port accounting) is
// implemented exactly once.
func startProxyEngine(key *VlessKey, singBoxPath, xrayPath string, port int, ctx context.Context, verbose, keepLogs bool) (func(), error) {
	// Xray-core is used for transports sing-box does not support natively (xhttp).
	useXray := key.Type == "xhttp"
	enginePath := singBoxPath
	if useXray {
		if xrayPath == "" {
			return nil, fmt.Errorf("XRAY_NOT_FOUND: xray binary required for xhttp keys")
		}
		enginePath = xrayPath
	}

	var configPath string
	var dataDir string
	if useXray {
		cfg, err := GenerateXrayConfig(key, port)
		if err != nil {
			return nil, fmt.Errorf("CONFIG_ERROR: %v", err)
		}
		configPath, err = WriteXrayConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("CONFIG_WRITE_ERROR: %v", err)
		}
	} else {
		cfg, err := GenerateConfig(key, port)
		if err != nil {
			return nil, fmt.Errorf("CONFIG_ERROR: %v", err)
		}
		configPath, err = WriteConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("CONFIG_WRITE_ERROR: %v", err)
		}
		dataDir = fmt.Sprintf("/tmp/vlesssub/data-%d", port)
		os.MkdirAll(dataDir, 0755)
	}

	var args []string
	if useXray {
		args = []string{"run", "-c", configPath}
	} else {
		args = []string{"run", "-c", configPath, "-D", dataDir}
	}

	cmd := exec.CommandContext(ctx, enginePath, args...)

	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		os.Remove(configPath)
		os.RemoveAll(dataDir)
		return nil, fmt.Errorf("SING_BOX_START_FAILED: %v", err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		time.Sleep(200 * time.Millisecond)
		if !keepLogs {
			os.Remove(configPath)
			os.RemoveAll(dataDir)
			logFile := fmt.Sprintf("/tmp/vlesssub/sing-box-%d.log", port)
			os.Remove(logFile)
		}
	}

	return cleanup, nil
}

func curlTargetURL(key *VlessKey, singBoxPath, xrayPath string, targetURL string, timeoutSec int, verbose bool, keepLogs bool) (status, reason string) {
	port, err := allocatePort()
	if err != nil {
		return "FAILED", fmt.Sprintf("NO_PORT: %v", err)
	}
	defer releasePort(port)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec+5)*time.Second)
	defer cancel()

	cleanup, err := startProxyEngine(key, singBoxPath, xrayPath, port, ctx, verbose, keepLogs)
	if err != nil {
		return "FAILED", err.Error()
	}
	defer cleanup()

	if !waitForPort(port, timeoutSec) {
		return "FAILED", "SING_BOX_START_FAILED: port not ready"
	}

	return runCurl(port, targetURL, timeoutSec, ctx)
}

func TestOneKey(idx int, key *VlessKey, singBoxPath, xrayPath string, timeoutSec int, verbose bool, keepLogs bool) TestResult {
	result := TestResult{
		KeyIdx:          idx,
		Key:             key,
		Remark:          key.Remark,
		IP:              key.Address,
		Status:          "FAILED",
		YoutubeStatus:   "FAILED",
		InstagramStatus: "FAILED",
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		result.YoutubeStatus, result.YoutubeReason = curlTargetURL(key, singBoxPath, xrayPath, "https://youtube.com", timeoutSec, verbose, keepLogs)
	}()

	go func() {
		defer wg.Done()
		result.InstagramStatus, result.InstagramReason = curlTargetURL(key, singBoxPath, xrayPath, "https://instagram.com", timeoutSec, verbose, keepLogs)
	}()

	wg.Wait()

	if result.YoutubeStatus == "OK" && result.InstagramStatus == "OK" {
		result.Status = "OK"
		result.Reason = ""
	} else {
		result.Status = "FAILED"
		var parts []string
		if result.YoutubeStatus != "OK" {
			parts = append(parts, fmt.Sprintf("youtube: %s", result.YoutubeReason))
		}
		if result.InstagramStatus != "OK" {
			parts = append(parts, fmt.Sprintf("instagram: %s", result.InstagramReason))
		}
		result.Reason = strings.Join(parts, "; ")
	}

	return result
}

func waitForPort(port int, timeoutSec int) bool {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func RunTests(keys []*VlessKey, singBoxPath, xrayPath string, timeoutSec int, maxParallel int, verbose bool, keepLogs bool) []TestResult {
	os.MkdirAll("/tmp/vlesssub", 0755)

	if maxParallel <= 0 || maxParallel > len(keys) {
		maxParallel = len(keys)
	}

	results := make([]TestResult, len(keys))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i, key := range keys {
		wg.Add(1)
		go func(idx int, k *VlessKey) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := TestOneKey(idx, k, singBoxPath, xrayPath, timeoutSec, verbose, keepLogs)
			results[idx] = result
		}(i, key)
	}

	wg.Wait()
	return results
}
