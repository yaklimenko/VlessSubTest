package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type TestRequest struct {
	URL      string `json:"url"`
	Timeout  int    `json:"timeout"`
	Parallel int    `json:"parallel"`
}

type TestResultItem struct {
	KeyIdx    int    `json:"key_idx"`
	IP        string `json:"ip,omitempty"`
	Remark    string `json:"remark,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Youtube   string `json:"youtube"`
	Instagram string `json:"instagram"`
}

type TestResponse struct {
	Total   int              `json:"total"`
	OK      int              `json:"ok"`
	Results []TestResultItem `json:"results"`
}

func startServer(singBoxPath, xrayPath string, timeoutSec, maxParallel int, verbose, keepLogs bool, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req TestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		if req.URL == "" {
			http.Error(w, `{"error":"url is required"}`, http.StatusBadRequest)
			return
		}

		if req.Timeout <= 0 {
			req.Timeout = timeoutSec
		}
		if req.Parallel <= 0 {
			req.Parallel = maxParallel
		}

		fmt.Fprintf(os.Stderr, "Fetching subscription from %s ...\n", req.URL)

		subscriptionLines, err := FetchSubscription(req.URL)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if len(subscriptionLines) == 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": "empty subscription"})
			return
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": "no valid vless keys found"})
			return
		}

		fmt.Fprintf(os.Stderr, "Found %d vless keys, testing...\n", len(keys))

		results := RunTests(keys, singBoxPath, xrayPath, req.Timeout, req.Parallel, verbose, keepLogs)

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

		resp := TestResponse{
			Results: make([]TestResultItem, 0, len(finalResults)),
		}
		resp.Total = len(finalResults)

		for _, r := range finalResults {
			item := TestResultItem{
				KeyIdx: r.KeyIdx,
				Status: r.Status,
				Reason: r.Reason,
			}
			if r.Key == nil {
				item.Youtube = "FAILED"
				item.Instagram = "FAILED"
			} else {
				item.IP = r.IP
				item.Remark = r.Remark
				item.Youtube = r.YoutubeDisplay()
				item.Instagram = r.InstagramDisplay()
				if r.Status == "OK" {
					resp.OK++
				}
			}
			resp.Results = append(resp.Results, item)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		handleProbe(w, r, singBoxPath, xrayPath, verbose, keepLogs)
	})

	mux.HandleFunc("/test-single", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Vless   string `json:"vless"`
			Timeout int    `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		if req.Vless == "" {
			http.Error(w, `{"error":"vless is required"}`, http.StatusBadRequest)
			return
		}

		if req.Timeout <= 0 {
			req.Timeout = timeoutSec
		}

		key, err := ParseVlessURI(req.Vless)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("parse error: %v", err)})
			return
		}

		result := TestOneKey(0, key, singBoxPath, xrayPath, req.Timeout, verbose, keepLogs)

		resp := TestResultItem{
			KeyIdx:    0,
			IP:        result.IP,
			Remark:    result.Remark,
			Status:    result.Status,
			Reason:    result.Reason,
			Youtube:   result.YoutubeDisplay(),
			Instagram: result.InstagramDisplay(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	addr := fmt.Sprintf(":%d", port)
	fmt.Fprintf(os.Stderr, "VlessSubTest server listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
