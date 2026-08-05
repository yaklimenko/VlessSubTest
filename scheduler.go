package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// CronConfig is the optional --cron-config JSON file:
//
//	{
//	  "subscriptions": [
//	    {"url": "https://.../sub/Example", "duration_sec": 180,
//	     "target_kbps": 4000, "parallel": 1, "probe_url": "https://.../tcb.mp4"}
//	  ]
//	}
//
// It is re-read before every scheduled run, so editing the file takes effect
// on the next tick.
type CronConfig struct {
	Subscriptions []CronSubscription `json:"subscriptions"`
}

type CronSubscription struct {
	URL         string `json:"url"`
	DurationSec int    `json:"duration_sec"`
	TargetKbps  int    `json:"target_kbps"`
	Parallel    int    `json:"parallel"`
	ProbeURL    string `json:"probe_url"`
}

// CronOptions carries the CLI flags that override the config file.
type CronOptions struct {
	ConfigPath string
	SubURL     string
	Duration   int
	TargetKbps int
	Parallel   int
	ProbeURL   string
}

// defaultCronSubURL is the aggregator subscription file built by the panel
// (https://example.com/sub/Example, URL-encoded).
const defaultCronSubURL = "https://example.com/sub/Example"

// cronEveryHours = 6 runs per day.
const cronEveryHours = 4

func defaultCronConfig() CronConfig {
	return CronConfig{
		Subscriptions: []CronSubscription{{
			URL:         defaultCronSubURL,
			DurationSec: probeDefaultDurationSec,
			TargetKbps:  probeDefaultTargetKbps,
			Parallel:    1,
			ProbeURL:    probeDefaultProbeURL,
		}},
	}
}

// loadCronConfig merges, in order: built-in defaults < config file < CLI flags.
// A --cron-sub flag replaces the whole subscription list with a single entry.
func loadCronConfig(o CronOptions) CronConfig {
	cfg := defaultCronConfig()

	if o.ConfigPath != "" {
		data, err := os.ReadFile(o.ConfigPath)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "cron: cannot read config file %s: %v\n", o.ConfigPath, err)
			}
		} else {
			var fc CronConfig
			if err := json.Unmarshal(data, &fc); err != nil || len(fc.Subscriptions) == 0 {
				fmt.Fprintf(os.Stderr, "cron: ignoring invalid config file %s\n", o.ConfigPath)
			} else {
				cfg = fc
			}
		}
	}

	if o.SubURL != "" {
		cfg.Subscriptions = []CronSubscription{{URL: o.SubURL}}
	}

	for i := range cfg.Subscriptions {
		s := &cfg.Subscriptions[i]
		if o.Duration > 0 {
			s.DurationSec = o.Duration
		} else if s.DurationSec <= 0 {
			s.DurationSec = probeDefaultDurationSec
		}
		if o.TargetKbps > 0 {
			s.TargetKbps = o.TargetKbps
		} else if s.TargetKbps <= 0 {
			s.TargetKbps = probeDefaultTargetKbps
		}
		if o.Parallel > 0 {
			s.Parallel = o.Parallel
		} else if s.Parallel <= 0 {
			s.Parallel = 1
		}
		if o.ProbeURL != "" {
			s.ProbeURL = o.ProbeURL
		} else if s.ProbeURL == "" {
			s.ProbeURL = probeDefaultProbeURL
		}
	}
	return cfg
}

// Scheduler runs the probe test on a 4-hour grid (6 runs/day). It is a single
// goroutine: one run at a time, missed ticks are not caught up.
type Scheduler struct {
	store       *RunStore
	opts        CronOptions
	singBoxPath string
	xrayPath    string
	verbose     bool
	keepLogs    bool
}

func NewScheduler(store *RunStore, opts CronOptions, singBoxPath, xrayPath string, verbose, keepLogs bool) *Scheduler {
	return &Scheduler{
		store:       store,
		opts:        opts,
		singBoxPath: singBoxPath,
		xrayPath:    xrayPath,
		verbose:     verbose,
		keepLogs:    keepLogs,
	}
}

// Start blocks until ctx is cancelled, running the probe every 4 hours.
func (s *Scheduler) Start(ctx context.Context) {
	for {
		timer := time.NewTimer(time.Until(nextCronTick(time.Now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.runAll(ctx, loadCronConfig(s.opts))
	}
}

// nextCronTick returns the next 4-hour boundary (00:00/04:00/08:00/12:00/
// 16:00/20:00 UTC) strictly after now.
func nextCronTick(now time.Time) time.Time {
	const every = cronEveryHours * time.Hour
	next := now.Truncate(every)
	if !next.After(now) {
		next = next.Add(every)
	}
	return next
}

func (s *Scheduler) runAll(ctx context.Context, cfg CronConfig) {
	for _, sub := range cfg.Subscriptions {
		if sub.URL == "" {
			continue
		}
		req := ProbeRequest{
			URL:         sub.URL,
			ProbeURL:    sub.ProbeURL,
			DurationSec: sub.DurationSec,
			TargetKbps:  sub.TargetKbps,
			Parallel:    sub.Parallel,
		}
		applyProbeDefaults(&req)

		fmt.Fprintf(os.Stderr, "cron: probe %s (duration=%ds target=%dkbps parallel=%d)\n",
			req.URL, req.DurationSec, req.TargetKbps, req.Parallel)

		startedAt := time.Now()
		resp, err := runProbeSubscription(ctx, req, s.singBoxPath, s.xrayPath, s.verbose, s.keepLogs)

		if s.store != nil {
			if serr := s.store.SaveRun(buildProbeRunRecord(req, startedAt, time.Now(), resp, err)); serr != nil {
				fmt.Fprintf(os.Stderr, "cron: failed to save run: %v\n", serr)
			}
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "cron: probe failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "cron: done %s: total=%d ok=%d degraded=%d failed=%d\n",
				req.URL, resp.Total, resp.OK, resp.Degraded, resp.Failed)
		}
	}
}
