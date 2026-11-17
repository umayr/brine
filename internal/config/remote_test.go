package config

import (
	"testing"
	"time"
)

func TestRemoteConfigurationFailsClosedUntilIntegration(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
	}{
		{"enabled", func(c *Config) { c.RemoteEnabled = true }},
		{"registry", func(c *Config) { c.RemoteRegistryDir = "/tmp/registry" }},
		{"forward", func(c *Config) { c.RemoteForwardDir = "/tmp/forward" }},
		{"timeout", func(c *Config) { c.RemoteTimeout = time.Second }},
		{"no auth", func(c *Config) { c.NoAuth = true; c.RemoteEnabled = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.set(c)
			if c.Validate() == nil {
				t.Fatal("unavailable remote configuration accepted")
			}
		})
	}
	if err := Default().Validate(); err != nil {
		t.Fatal("default changed", err)
	}
}
