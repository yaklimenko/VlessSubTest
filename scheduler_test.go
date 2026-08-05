package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextCronTick(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-08-06T00:30:00Z", "2026-08-06T04:00:00Z"},
		{"2026-08-06T04:00:00Z", "2026-08-06T08:00:00Z"},
		{"2026-08-06T03:59:59Z", "2026-08-06T04:00:00Z"},
		{"2026-08-06T23:59:59Z", "2026-08-07T00:00:00Z"},
		{"2026-08-06T20:00:01Z", "2026-08-07T00:00:00Z"},
	}
	layout := "2006-01-02T15:04:05Z"
	for _, c := range cases {
		in, _ := time.Parse(layout, c.in)
		want, _ := time.Parse(layout, c.want)
		got := nextCronTick(in)
		if !got.Equal(want) {
			t.Errorf("nextCronTick(%s) = %s, want %s", c.in, got.UTC().Format(layout), c.want)
		}
	}
}

func TestLoadCronConfigDefaults(t *testing.T) {
	cfg := loadCronConfig(CronOptions{})
	if len(cfg.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(cfg.Subscriptions))
	}
	s := cfg.Subscriptions[0]
	if s.URL != defaultCronSubURL {
		t.Errorf("url = %s, want %s", s.URL, defaultCronSubURL)
	}
	if s.DurationSec != probeDefaultDurationSec || s.TargetKbps != probeDefaultTargetKbps {
		t.Errorf("bad defaults: %+v", s)
	}
}

func TestLoadCronConfigFileAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cron.json")
	os.WriteFile(path, []byte(`{"subscriptions":[{"url":"https://x.example/sub","duration_sec":60,"target_kbps":1000}]}`), 0644)

	cfg := loadCronConfig(CronOptions{ConfigPath: path})
	s := cfg.Subscriptions[0]
	if s.URL != "https://x.example/sub" || s.DurationSec != 60 || s.TargetKbps != 1000 {
		t.Errorf("file not applied: %+v", s)
	}
	if s.Parallel == 0 || s.ProbeURL == "" {
		t.Errorf("file defaults not filled: %+v", s)
	}

	cfg = loadCronConfig(CronOptions{ConfigPath: path, SubURL: "https://y.example/sub", Duration: 90})
	if len(cfg.Subscriptions) != 1 || cfg.Subscriptions[0].URL != "https://y.example/sub" || cfg.Subscriptions[0].DurationSec != 90 {
		t.Errorf("overrides not applied: %+v", cfg.Subscriptions)
	}
}
