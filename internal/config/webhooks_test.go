package config

import (
	"testing"
	"time"
)

func TestWebhookSettingsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		valid  bool
	}{
		{"off", func(*Config) {}, true},
		{"orphan path", func(c *Config) { c.WebhooksDir = "/private/state" }, false},
		{"valid", func(c *Config) {
			c.WebhooksEnabled = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/private/keys/hooks"
		}, true},
		{"missing key", func(c *Config) { c.WebhooksEnabled = true; c.WebhooksDir = "/private/state" }, false},
		{"relative", func(c *Config) { c.WebhooksEnabled = true; c.WebhooksDir = "state"; c.WebhooksKeyFile = "/private/key" }, false},
		{"no auth", func(c *Config) {
			c.WebhooksEnabled = true
			c.NoAuth = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/private/key"
		}, false},
		{"key in data", func(c *Config) {
			c.WebhooksEnabled = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/data/key"
		}, false},
		{"key in state", func(c *Config) {
			c.WebhooksEnabled = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/private/state/key"
		}, false},
		{"retention", func(c *Config) {
			c.WebhooksEnabled = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/private/key"
			c.WebhooksRetention = time.Hour
		}, false},
		{"unsafe cidr", func(c *Config) {
			c.WebhooksEnabled = true
			c.WebhooksDir = "/private/state"
			c.WebhooksKeyFile = "/private/key"
			c.WebhooksAllowPrivateCIDRs = []string{"0.0.0.0/0"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.DataDir = "/data"
			tc.change(c)
			if e := c.Validate(); (e == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, e)
			}
		})
	}
}
