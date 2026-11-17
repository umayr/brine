package config

import (
	"strings"
	"testing"
)

func TestDisabledFeatureConfiguration(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"alpha":         func(c *Config) { c.Profile = "alpha" },
		"secret target": func(c *Config) { c.SecretTarget = "test" },
		"secret policy": func(c *Config) { c.SecretPolicy = "/tmp/policy" },
		"secret pkl":    func(c *Config) { c.SecretPkl = "/tmp/pkl" },
		"history":       func(c *Config) { c.OperationStateDir = "/tmp/history" },
		"coordination":  func(c *Config) { c.CoordinationStateDir = "/tmp/state" },
	} {
		t.Run(name, func(t *testing.T) {
			c := Default()
			change(c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "disabled") {
				t.Fatalf("got %v", err)
			}
		})
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("legacy: %v", err)
	}
}
