package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func operationConfig() *Config {
	c := Default()
	c.Profile = "alpha"
	c.DillBin = "/synthetic/operator-selected/b3/dill"
	c.EngineSocket = "/synthetic/docker.sock"
	c.DataDir = "/synthetic/brine-data"
	c.CoordinationStateDir = "/synthetic/private-state"
	c.CoordinationLayout = "native-local-docker-v1"
	c.CoordinationDaemonID = "independently-known-daemon-1234"
	return c
}

func TestCoordinationConfigurationFailsClosed(t *testing.T) {
	cases := []struct {
		name, contains string
		change         func(*Config)
	}{
		{"partial-state", "all three", func(c *Config) { c.CoordinationLayout = "" }},
		{"partial-daemon", "all three", func(c *Config) { c.CoordinationDaemonID = "" }},
		{"legacy", "authenticated Alpha", func(c *Config) { c.Profile = "legacy" }},
		{"podman", "Docker", func(c *Config) { c.Engine = "podman" }},
		{"noauth", "authentication", func(c *Config) { c.NoAuth = true }},
		{"bundled-binary", "operator-selected", func(c *Config) { c.DillBin = "dill" }},
		{"layout", "layout", func(c *Config) { c.CoordinationLayout = "desktop" }},
		{"relative-state", "canonical", func(c *Config) { c.CoordinationStateDir = "state" }},
		{"unclean-state", "canonical", func(c *Config) { c.CoordinationStateDir = "/private/../state" }},
		{"stack-editable-state", "outside", func(c *Config) { c.CoordinationStateDir = "/synthetic/brine-data/coordination" }},
		{"secret-mode", "secret", func(c *Config) { c.SecretTarget = "test" }},
		{"daemon-whitespace", "daemon ID", func(c *Config) { c.CoordinationDaemonID = "invalid daemon" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := operationConfig()
			tc.change(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("want %s rejection, got %v", tc.contains, err)
			}
		})
	}
	c := operationConfig()
	err := c.Validate()
	if runtime.GOOS != "linux" && (err == nil || !strings.Contains(err.Error(), "native Linux")) {
		t.Fatalf("unsupported host accepted: %v", err)
	}
	if runtime.GOOS == "linux" && err == nil {
		t.Fatal("nonexistent state provisioned/accepted")
	}
}

func TestCoordinationDoesNotCreateState(t *testing.T) {
	c := operationConfig()
	c.CoordinationStateDir = filepath.Join(t.TempDir(), "must-not-create")
	_ = c.Validate()
	if _, err := os.Lstat(c.CoordinationStateDir); !os.IsNotExist(err) {
		t.Fatalf("state created: %v", err)
	}
}

func TestCoordinationRejectsDataAlias(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(data, alias); err != nil {
		t.Fatal(err)
	}
	c := operationConfig()
	c.DataDir = alias
	canonicalData, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	c.CoordinationStateDir = filepath.Join(canonicalData, "state")
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("data alias accepted: %v", err)
	}
}
