package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

func startServer(singBoxPath, xrayPath string, timeoutSec, maxParallel int, verbose, keepLogs bool, port int, store *RunStore, sched *Scheduler) {
	if sched != nil {
		go sched.Start(context.Background())
	}

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

		startedAt := time.Now()

		fmt.Fprintf(os.Stderr, "Fetching subscription from %s ...\n", req.URL)

		subscriptionLines, err := FetchSubscription(req.URL)
		if err != nil {
			if store != nil {
				store.SaveRun(buildTestRunRecord(req.URL, startedAt, time.Now(), TestResponse{}, err))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if len(subscriptionLines) == 0 {
			if store != nil {
				store.SaveRun(buildTestRunRecord(req.URL, startedAt, time.Now(), TestResponse{}, fmt.Errorf("empty subscription")))
			}
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
			if store != nil {
				store.SaveRun(buildTestRunRecord(req.URL, startedAt, time.Now(), TestResponse{}, fmt.Errorf("no valid vless keys found")))
			}
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

		if store != nil {
			if serr := store.SaveRun(buildTestRunRecord(req.URL, startedAt, time.Now(), resp, nil)); serr != nil {
				fmt.Fprintf(os.Stderr, "test: failed to save run: %v\n", serr)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		handleProbe(w, r, store, singBoxPath, xrayPath, verbose, keepLogs)
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

	// GET /runs — accumulated run history with optional date range.
	mux.HandleFunc("/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"total": 0, "runs": []RunRecord{}})
			return
		}

		q := r.URL.Query()

		from, err := parseTimeParam(q.Get("from"), false)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		to, err := parseTimeParam(q.Get("to"), true)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if from != nil && to != nil && from.After(*to) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from must be <= to"})
			return
		}

		limit := 100
		if v := q.Get("limit"); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
				return
			}
			limit = n
		}
		if limit > 1000 {
			limit = 1000
		}

		detail := q.Get("detail") == "1"

		runs, err := store.ListRuns(from, to, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if runs == nil {
			runs = []RunRecord{}
		}

		// Without detail=1, strip per-key results for a lighter list.
		if !detail {
			for i := range runs {
				runs[i].Results = nil
			}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"total": len(runs), "runs": runs})
	})

	// GET /runs/{id} — a single stored run.
	mux.HandleFunc("/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/runs/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "run id is required"})
			return
		}
		if store == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		rec, err := store.GetRun(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if rec == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	addr := fmt.Sprintf(":%d", port)
	fmt.Fprintf(os.Stderr, "VlessSubTest server listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// parseTimeParam parses an optional time query param. Accepts RFC3339 /
// RFC3339Nano timestamps and plain dates (YYYY-MM-DD); for date-only values
// endOfDay=true makes the range end at 23:59:59.999999999.
func parseTimeParam(s string, endOfDay bool) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return &t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	return nil, fmt.Errorf("invalid time %q (use YYYY-MM-DD or RFC3339)", s)
}
