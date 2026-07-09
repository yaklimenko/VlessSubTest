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
	KeyIdx          int
	Key             *VlessKey
	Remark          string
	IP              string
	Status          string // "OK" if both services OK, else "FAILED"
	Reason          string
	YoutubeStatus   string
	YoutubeReason   string
	InstagramStatus string
	InstagramReason string
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

func curlTargetURL(key *VlessKey, singBoxPath string, targetURL string, timeoutSec int, verbose bool, keepLogs bool) (status, reason string) {
	port, err := allocatePort()
	if err != nil {
		return "FAILED", fmt.Sprintf("NO_PORT: %v", err)
	}
	defer releasePort(port)

	cfg, err := GenerateConfig(key, port)
	if err != nil {
		return "FAILED", fmt.Sprintf("CONFIG_ERROR: %v", err)
	}

	configPath, err := WriteConfig(cfg)
	if err != nil {
		return "FAILED", fmt.Sprintf("CONFIG_WRITE_ERROR: %v", err)
	}

	dataDir := fmt.Sprintf("/tmp/vlesssub/data-%d", port)
	os.MkdirAll(dataDir, 0755)

	cleanup := func() {
		if !keepLogs {
			os.Remove(configPath)
			os.RemoveAll(dataDir)
			logFile := fmt.Sprintf("/tmp/vlesssub/sing-box-%d.log", port)
			os.Remove(logFile)
		}
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec+5)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, singBoxPath, "run",
		"-c", configPath,
		"-D", dataDir,
	)

	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		return "FAILED", fmt.Sprintf("SING_BOX_START_FAILED: %v", err)
	}

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		time.Sleep(200 * time.Millisecond)
	}()

	if !waitForPort(port, timeoutSec) {
		return "FAILED", "SING_BOX_START_FAILED: port not ready"
	}

	return runCurl(port, targetURL, timeoutSec, ctx)
}

func TestOneKey(idx int, key *VlessKey, singBoxPath string, timeoutSec int, verbose bool, keepLogs bool) TestResult {
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
		result.YoutubeStatus, result.YoutubeReason = curlTargetURL(key, singBoxPath, "https://youtube.com", timeoutSec, verbose, keepLogs)
	}()

	go func() {
		defer wg.Done()
		result.InstagramStatus, result.InstagramReason = curlTargetURL(key, singBoxPath, "https://instagram.com", timeoutSec, verbose, keepLogs)
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

func RunTests(keys []*VlessKey, singBoxPath string, timeoutSec int, maxParallel int, verbose bool, keepLogs bool) []TestResult {
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

			result := TestOneKey(idx, k, singBoxPath, timeoutSec, verbose, keepLogs)
			results[idx] = result
		}(i, key)
	}

	wg.Wait()
	return results
}
