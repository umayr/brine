package config

import (
	"path/filepath"
	"testing"
)

func TestSecretModeRequiresExplicitTrustedConfiguration(t *testing.T) {
	base := Default()
	base.DataDir = filepath.Join(t.TempDir(), "brine")
	base.SecretTarget = "lab-a"
	base.SecretPolicy = filepath.Join(t.TempDir(), "policy.json")
	base.EngineSocket = "unix:///run/docker.sock"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.SecretTarget = "" }, func(c *Config) { c.SecretTarget = "BAD" }, func(c *Config) { c.SecretPolicy = "relative" }, func(c *Config) { c.EngineSocket = "" }, func(c *Config) { c.EngineSocket = "tcp://example:2375" }, func(c *Config) { c.SecretPkl = "relative" }, func(c *Config) { c.SecretPolicy = filepath.Join(c.DataDir, "policy.json") }} {
		c := *base
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("invalid secret configuration accepted")
		}
	}
}
