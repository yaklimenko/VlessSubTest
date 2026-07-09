package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var url string
	timeoutSec := 10
	maxParallel := 0 // 0 = all
	verbose := false
	keepLogs := false

	// Parse args
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "url=") {
			url = strings.TrimPrefix(arg, "url=")
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
		} else if strings.HasPrefix(arg, "--") {
			fmt.Fprintf(os.Stderr, "Error: unknown option: %s\n", arg)
			os.Exit(1)
		} else {
			fmt.Fprintf(os.Stderr, "Error: unexpected argument: %s\n", arg)
			os.Exit(1)
		}
	}

	if url == "" {
		fmt.Fprintf(os.Stderr, "Error: url= is required\n")
		os.Exit(1)
	}

	// Find sing-box binary
	execDir, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot determine executable path: %v\n", err)
		os.Exit(1)
	}
	execDir = filepath.Dir(execDir)

	singBoxPath := filepath.Join(execDir, "sing-box")
	if _, err := os.Stat(singBoxPath); os.IsNotExist(err) {
		// Try current directory
		singBoxPath = "sing-box"
		if _, err := os.Stat(singBoxPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: sing-box binary not found. Place it next to vlesssubtest or in PATH.\n")
			os.Exit(1)
		}
	}

	// Fetch subscription
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

	// Parse vless keys
	var keys []*VlessKey
	var preResults []TestResult // for keys that fail to parse at this stage

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

	// Run tests
	results := RunTests(keys, singBoxPath, timeoutSec, maxParallel, verbose, keepLogs)

	// Merge pre-results (parse failures)
	finalResults := make([]TestResult, 0, len(results)+len(preResults))
	keyIdx := 0
	preIdx := 0

	// Reconstruct order: walk original lines, match to results or preResults
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
  vlesssubtest url=<subscription_url> [options]

Options:
  url=<url>          Subscription URL (required)
  --timeout=N        Test timeout in seconds (default: 10)
  --parallel=N       Max parallel tests (default: all)
  --verbose          Show sing-box logs on error
  --keep-logs        Keep temporary files in /tmp/vlesssub
  --help, -h         Show this help

Example:
  vlesssubtest url=https://example.com/sub/ExampleClient
  vlesssubtest url=https://example.com/sub --timeout=15 --parallel=5`)
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
