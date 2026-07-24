package main

import (
	"fmt"
	"io"
	"net/http"
)

func PrintResults(results []TestResult) {
	okCount := 0
	total := len(results)

	for _, r := range results {
		if r.Status == "OK" {
			okCount++
		}
	}

	fmt.Printf("\nvlesssubtest results: %d/%d OK\n\n", okCount, total)

	for _, r := range results {
		switch {
		case r.Key == nil:
			fmt.Printf("keyIdx: %d | FAILED to parse\n", r.KeyIdx)
		default:
			fmt.Printf("keyIdx: %d | %s | %s | youtube: %s; instagram: %s\n",
				r.KeyIdx, r.IP, r.Remark, r.YoutubeDisplay(), r.InstagramDisplay())
		}
	}
}

func FetchSubscription(url string) ([]string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("request error: %w", err)
	}
	req.Header.Set("User-Agent", "vlesssubtest/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subscription unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription unreachable: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	return decodeBase64Subscription(string(body))
}
