package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// RunRecord is one persisted test run (kind: "test" or "probe"). The bucket
// key is the fixed-width UTC start timestamp, so keys sort chronologically and
// date-range queries are a simple cursor scan (no full-table pass).
type RunRecord struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"` // "test" | "probe"
	SubscriptionURL string          `json:"subscription_url"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      time.Time       `json:"finished_at"`
	DurationSec     int             `json:"duration_sec"`
	TargetKbps      int             `json:"target_kbps,omitempty"`
	ProbeURL        string          `json:"probe_url,omitempty"`
	Parallel        int             `json:"parallel,omitempty"`
	Total           int             `json:"total"`
	OK              int             `json:"ok"`
	Degraded        int             `json:"degraded,omitempty"`
	Failed          int             `json:"failed"`
	Results         json.RawMessage `json:"results,omitempty"`
	Error           string          `json:"error,omitempty"`
}

// runKeyFormat is fixed-width (9 fractional digits, always padded) so that
// string ordering equals chronological ordering. time.RFC3339Nano is NOT safe
// here because it trims trailing zeros, breaking lexicographic order.
const runKeyFormat = "2006-01-02T15:04:05.000000000Z07:00"

const runsBucket = "runs"

type RunStore struct {
	db *bolt.DB
}

// OpenStore opens (or creates) the bbolt database at path and ensures the
// "runs" bucket exists. Parent directories are created if missing.
func OpenStore(path string) (*RunStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := bolt.Open(path, 0644, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(runsBucket))
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &RunStore{db: db}, nil
}

func (s *RunStore) Close() error {
	return s.db.Close()
}

// SaveRun persists a run. ID is derived from the start timestamp; a numeric
// suffix is appended if the key somehow already exists.
func (s *RunStore) SaveRun(rec *RunRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(runsBucket))
		key := []byte(rec.StartedAt.UTC().Format(runKeyFormat))
		for i := 1; b.Get(key) != nil; i++ {
			key = []byte(fmt.Sprintf("%s-%d", rec.StartedAt.UTC().Format(runKeyFormat), i))
		}
		rec.ID = string(key)
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

// ListRuns returns runs whose start time falls in [from, to] (both inclusive,
// nil = unbounded), newest first, capped at limit (default 100).
func (s *RunStore) ListRuns(from, to *time.Time, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	var fromKey, toKey []byte
	if from != nil {
		fromKey = []byte(from.UTC().Format(runKeyFormat))
	}
	if to != nil {
		toKey = []byte(to.UTC().Format(runKeyFormat))
	}

	var runs []RunRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(runsBucket)).Cursor()
		var k, v []byte
		if len(fromKey) > 0 {
			k, v = c.Seek(fromKey)
		} else {
			k, v = c.First()
		}
		for ; k != nil; k, v = c.Next() {
			if len(toKey) > 0 && string(k) > string(toKey) {
				break
			}
			var rec RunRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			runs = append(runs, rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Newest first.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// GetRun returns a single run by ID, or nil if not found.
func (s *RunStore) GetRun(id string) (*RunRecord, error) {
	var rec *RunRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(runsBucket)).Get([]byte(id))
		if v == nil {
			return nil
		}
		var r RunRecord
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		rec = &r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// buildTestRunRecord wraps a /test response into a persisted RunRecord.
func buildTestRunRecord(url string, startedAt, finishedAt time.Time, resp TestResponse, runErr error) *RunRecord {
	rec := &RunRecord{
		Kind:            "test",
		SubscriptionURL: url,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationSec:     int(finishedAt.Sub(startedAt).Seconds()),
		Total:           resp.Total,
		OK:              resp.OK,
		Failed:          resp.Total - resp.OK,
	}
	if runErr != nil {
		rec.Error = runErr.Error()
		return rec
	}
	if data, err := json.Marshal(resp.Results); err == nil {
		rec.Results = data
	}
	return rec
}

// buildProbeRunRecord wraps a /probe response into a persisted RunRecord.
func buildProbeRunRecord(req ProbeRequest, startedAt, finishedAt time.Time, resp ProbeResponse, runErr error) *RunRecord {
	rec := &RunRecord{
		Kind:            "probe",
		SubscriptionURL: req.URL,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		DurationSec:     int(finishedAt.Sub(startedAt).Seconds()),
		TargetKbps:      req.TargetKbps,
		ProbeURL:        req.ProbeURL,
		Parallel:        req.Parallel,
		Total:           resp.Total,
		OK:              resp.OK,
		Degraded:        resp.Degraded,
		Failed:          resp.Failed,
	}
	if runErr != nil {
		rec.Error = runErr.Error()
		return rec
	}
	if data, err := json.Marshal(resp.Results); err == nil {
		rec.Results = data
	}
	return rec
}
