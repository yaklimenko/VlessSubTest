package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var url string
	timeoutSec := 10
	maxParallel := 0
	verbose := false
	keepLogs := false
	serverPort := 8080
	hasURL := false

	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "url=") {
			url = strings.TrimPrefix(arg, "url=")
			hasURL = true
		} else if arg == "--help" || arg == "-h" {
			printUsage()
			os.Exit(0)
		} else if arg == "--verbose" {
			verbose = true
		} else if arg == "--keep-logs" {
			keepLogs = true
		} else if strings.HasPrefix(arg, "--timeout=") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(arg, "--timeout="), "%d", &timeoutSec); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --timeout value\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--parallel=") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(arg, "--parallel="), "%d", &maxParallel); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --parallel value\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--port=") {
			if _, err := fmt.Sscanf(strings.TrimPrefix(arg, "--port="), "%d", &serverPort); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --port value\n")
				os.Exit(1)
			}
		} else if strings.HasPrefix(arg, "--") {
			fmt.Fprintf(os.Stderr, "Error: unknown option: %s\n", arg)
			os.Exit(1)
		} else {
			fmt.Fprintf(os.Stderr, "Error: unexpected argument: %s\n", arg)
			os.Exit(1)
		}
	}

	singBoxPath := findSingBox()

	if hasURL {
		runCLI(url, singBoxPath, timeoutSec, maxParallel, verbose, keepLogs)
	} else {
		startServer(singBoxPath, timeoutSec, maxParallel, verbose, keepLogs, serverPort)
	}
}

func findSingBox() string {
	execDir, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine executable path: %v\n", err)
		os.Exit(1)
	}
	execDir = filepath.Dir(execDir)

	singBoxPath := filepath.Join(execDir, "sing-box")
	if _, err := os.Stat(singBoxPath); os.IsNotExist(err) {
		singBoxPath = "sing-box"
		if _, err := os.Stat(singBoxPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: sing-box binary not found. Place it next to vlesssubtest or in PATH.\n")
			os.Exit(1)
		}
	}
	return singBoxPath
}

func runCLI(url, singBoxPath string, timeoutSec, maxParallel int, verbose, keepLogs bool) {
	if url == "" {
		fmt.Fprintf(os.Stderr, "Error: url= is required\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Fetching subscription from %s ...\n", url)
	subscriptionLines, err := FetchSubscription(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(subscriptionLines) == 0 {
		fmt.Fprintf(os.Stderr, "Error: empty subscription\n")
		os.Exit(1)
	}

	var keys []*VlessKey
	var preResults []TestResult

	for i, line := range subscriptionLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, err := ParseVlessURI(line)
		if err != nil {
			preResults = append(preResults, TestResult{
				KeyIdx: i,
				Status: "FAILED",
				Reason: "parse_error",
			})
			continue
		}
		keys = append(keys, key)
	}

	if len(keys) == 0 && len(preResults) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no valid vless keys found\n")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Found %d vless keys, testing...\n", len(keys))

	results := RunTests(keys, singBoxPath, timeoutSec, maxParallel, verbose, keepLogs)

	finalResults := make([]TestResult, 0, len(results)+len(preResults))
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

	PrintResults(finalResults)
}

func printUsage() {
	fmt.Println(`vlesssubtest — test VLESS subscription keys against youtube.com and instagram.com

Usage:
  vlesssubtest url=<subscription_url> [options]     CLI mode: test and exit
  vlesssubtest [--port=N] [options]                 Server mode: HTTP API

Options:
  url=<url>          Subscription URL (required in CLI mode)
  --timeout=N        Test timeout in seconds (default: 10)
  --parallel=N       Max parallel tests (default: all)
  --port=N           HTTP server port (default: 8080)
  --verbose          Show sing-box logs on error
  --keep-logs        Keep temporary files in /tmp/vlesssub
  --help, -h         Show this help

CLI mode:
  vlesssubtest url=https://example.com/sub/ExampleClient
  vlesssubtest url=https://example.com/sub --timeout=15 --parallel=5

Server mode:
  vlesssubtest --port=8080
  curl -X POST http://localhost:8080/test -H 'Content-Type: application/json' -d '{"url":"https://example.com/sub"}'`)
}

// decodeBase64Subscription decodes a base64-encoded subscription into lines.
func decodeBase64Subscription(data string) ([]string, error) {
	data = strings.TrimSpace(data)

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Try URL-safe base64
		decoded, err = base64.URLEncoding.DecodeString(data)
		if err != nil {
			// Try with padding
			decoded, err = base64.StdEncoding.DecodeString(data + strings.Repeat("=", 4-len(data)%4))
			if err != nil {
				return nil, fmt.Errorf("base64 decode error: %w", err)
			}
		}
	}

	lines := strings.Split(string(decoded), "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}
