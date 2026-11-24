package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/config"
)

func TestHistoryStartupFlagsRequireExplicitCapacity(t *testing.T) {
	root := t.TempDir()
	mkdir := func(name string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	history, data, registry := mkdir("history"), mkdir("data"), mkdir("registry")
	parse := func(args ...string) *config.Config {
		t.Helper()
		cfg := &config.Config{RemoteEnabled: true, DataDir: data, RemoteRegistryDir: registry}
		flags := flag.NewFlagSet("synthetic history options", flag.ContinueOnError)
		registerMetricsHistoryFlags(flags, cfg)
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if path, err := parse().MetricsHistoryStartupDir(); err != nil || path != "" {
		t.Fatalf("defaults enabled history: %v", err)
	}
	for _, args := range [][]string{
		{"--metrics-history-dir", history},
		{"--metrics-history-max-pages", "1024"},
		{"--metrics-history-enabled", "--metrics-history-dir", history},
	} {
		if _, err := parse(args...).MetricsHistoryStartupDir(); !errors.Is(err, config.ErrMetricsHistorySettings) {
			t.Fatalf("incomplete options accepted: %v", err)
		}
	}
	cfg := parse("--metrics-history-enabled", "--metrics-history-dir", history, "--metrics-history-max-pages", "1024")
	if path, err := cfg.MetricsHistoryStartupDir(); err != nil || path != history || cfg.MetricsHistoryMaxPages != 1024 {
		t.Fatalf("complete options not read: %v", err)
	}
	if entries, err := os.ReadDir(history); err != nil || len(entries) != 0 {
		t.Fatal("flags created a store")
	}
}
