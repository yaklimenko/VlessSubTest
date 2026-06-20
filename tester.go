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

// TestResult holds the outcome of testing one vless key.
type TestResult struct {
	KeyIdx int
	Key    *VlessKey
	Remark string
	IP     string
	Status string // "OK" or "FAILED"
	Reason string // e.g. "TIMEOUT", "HTTP_403", platform error
	Port   int
}

// portRange defines the range of ports available for socks5.
const (
	portRangeStart = 10800
	portRangeEnd   = 10900
)

var (
	portMutex    sync.Mutex
	usedPorts    = map[int]bool{}
	portNext     = portRangeStart
)

// allocatePort finds a free port in the range.
func allocatePort() (int, error) {
	portMutex.Lock()
	defer portMutex.Unlock()

	for i := portNext; i <= portRangeEnd; i++ {
		if usedPorts[i] {
			continue
		}
		// Check if port is actually available
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

// releasePort marks a port as available again.
func releasePort(port int) {
	portMutex.Lock()
	defer portMutex.Unlock()
	delete(usedPorts, port)
	if port < portNext {
		portNext = port
	}
}

// TestOneKey tests a single vless key by running sing-box and curling through it.
func TestOneKey(idx int, key *VlessKey, singBoxPath string, timeoutSec int, verbose bool, keepLogs bool) TestResult {
	result := TestResult{
		KeyIdx: idx,
		Key:    key,
		Remark: key.Remark,
		IP:     key.Address,
		Port:   0,
		Status: "FAILED",
	}

	// Allocate port
	port, err := allocatePort()
	if err != nil {
		result.Reason = fmt.Sprintf("NO_PORT: %v", err)
		return result
	}
	result.Port = port
	defer releasePort(port)

	// Generate and write config
	cfg, err := GenerateConfig(key, port)
	if err != nil {
		result.Reason = fmt.Sprintf("CONFIG_ERROR: %v", err)
		return result
	}

	configPath, err := WriteConfig(cfg)
	if err != nil {
		result.Reason = fmt.Sprintf("CONFIG_WRITE_ERROR: %v", err)
		return result
	}

	// Create data directory
	dataDir := fmt.Sprintf("/tmp/vlesssub/data-%d", port)
	os.MkdirAll(dataDir, 0755)

	// Cleanup function
	cleanup := func() {
		if !keepLogs {
			os.Remove(configPath)
			os.RemoveAll(dataDir)
			logFile := fmt.Sprintf("/tmp/vlesssub/sing-box-%d.log", port)
			os.Remove(logFile)
		}
	}
	defer cleanup()

	// Start sing-box
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
		result.Reason = fmt.Sprintf("SING_BOX_START_FAILED: %v", err)
		return result
	}

	// Ensure sing-box is killed on exit
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		// Wait a bit for cleanup
		time.Sleep(200 * time.Millisecond)
	}()

	// Wait for socks5 port to be ready
	if !waitForPort(port, timeoutSec) {
		result.Reason = "SING_BOX_START_FAILED: port not ready"
		return result
	}

	// Run curl test
	curlCtx, curlCancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer curlCancel()

	curlCmd := exec.CommandContext(curlCtx, "curl",
		"-x", fmt.Sprintf("socks5h://127.0.0.1:%d", port),
		"-sS",
		"-o", "/dev/null",
		"-w", "%{http_code}",
		"--connect-timeout", fmt.Sprintf("%d", timeoutSec/2+2),
		"--max-time", fmt.Sprintf("%d", timeoutSec),
		"https://youtube.com",
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
			// We got an HTTP response but curl had some issue
			code := strings.TrimSpace(httpCode)
			// If time exceeded -> TIMEOUT
			if ctx.Err() != nil || curlCtx.Err() != nil {
				result.Reason = "TIMEOUT"
			} else if code == "000" {
				result.Reason = "CONNECTION_FAILED"
			} else {
				result.Reason = fmt.Sprintf("CURL_EXIT_%d", exitCode)
			}
			return result
		}
		// Curl error
		if exitErr != nil {
			errStr := err.Error()
			switch {
			case strings.Contains(errStr, "timed out") || strings.Contains(errStr, "timeout"):
				result.Reason = "TIMEOUT"
			case strings.Contains(errStr, "refused") || strings.Contains(errStr, "reset") || strings.Contains(errStr, "Could not resolve"):
				result.Reason = "CONNECTION_FAILED"
			default:
				result.Reason = fmt.Sprintf("CURL_EXIT_%d", exitCode)
			}
			return result
		}
	}

	httpCode := strings.TrimSpace(string(output))

	if httpCode == "" || httpCode == "000" {
		result.Reason = "CONNECTION_FAILED"
		return result
	}

	// Check HTTP status
	switch {
	case httpCode == "200":
		result.Status = "OK"
		result.Reason = ""
	case httpCode == "301" || httpCode == "302" || httpCode == "307" || httpCode == "308":
		// Redirect is OK - YouTube might redirect
		result.Status = "OK"
		result.Reason = ""
	case httpCode[0] == '2':
		result.Status = "OK"
		result.Reason = ""
	default:
		result.Reason = fmt.Sprintf("HTTP_%s", httpCode)
	}

	return result
}

// waitForPort polls until the given port is accepting connections.
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

// RunTests orchestrates parallel testing of all keys.
func RunTests(keys []*VlessKey, singBoxPath string, timeoutSec int, maxParallel int, verbose bool, keepLogs bool) []TestResult {
	// Ensure temp dir exists
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
