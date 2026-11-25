package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/config"
)

func TestRestartMetadataFlagsExplicitAndBounded(t *testing.T) {
	cfg := &config.Config{}
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	registerRestartMetadataFlags(flags, cfg)
	if cfg.RestartMetadata.Enabled || cfg.RestartMetadata.MaxBytes != 0 || cfg.RestartMetadata.Enrollments != nil {
		t.Fatal("flags enabled defaults")
	}
	args := []string{"-restart-metadata-enabled", "-restart-metadata-dir=/missing/archive", "-restart-metadata-key-file=/missing/keys/key", "-restart-metadata-store-id=" + strings.Repeat("ab", 32), "-restart-metadata-max-bytes=1024", "-restart-metadata-max-operations=10", "-restart-metadata-enrollment=host=" + strings.Repeat("cd", 16)}
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	cfg.RemoteEnabled = true
	cfg.DataDir = "/missing/workspace"
	cfg.RemoteRegistryDir = "/missing/registry"
	if _, pins, err := cfg.RestartMetadataStartup(); err != nil || len(pins) != 1 {
		t.Fatal("flags did not produce validated startup settings")
	}
	for len(cfg.RestartMetadata.Enrollments) < 64 {
		if err := flags.Set("restart-metadata-enrollment", "host="+strings.Repeat("cd", 16)); err != nil {
			t.Fatal(err)
		}
	}
	if err := flags.Set("restart-metadata-enrollment", "extra"); err == nil {
		t.Fatal("unbounded approvals")
	}
}
