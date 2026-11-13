package config

import (
	"strings"
	"testing"
)

func TestAlphaRequiresAuthenticationWithoutSecretOptions(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "::1"} {
		c := Default()
		c.Bind = bind
		c.Profile = "alpha"
		c.EngineSocket = "/synthetic/selected.sock"
		c.NoAuth = true
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "requires authentication") {
			t.Fatalf("Alpha no-auth accepted on %s: %v", bind, err)
		}
		c.NoAuth = false
		if err := c.Validate(); err != nil {
			t.Fatalf("authenticated Alpha rejected: %v", err)
		}
		c.Profile = "legacy"
		c.NoAuth = true
		if err := c.Validate(); err != nil {
			t.Fatalf("legacy loopback compatibility changed: %v", err)
		}
	}
}
