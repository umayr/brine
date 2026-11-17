package config

import (
	"testing"
	"time"
)

func TestRemoteOptInRequiresCompleteExplicitConfig(t *testing.T) {
	good := func() *Config {
		c := Default()
		c.RemoteEnabled = true
		c.RemoteRegistryDir = "/private/registry"
		c.RemoteForwardDir = "/private/forward"
		c.RemoteTimeout = time.Second
		return c
	}
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){"no auth": func(c *Config) { c.NoAuth = true }, "no registry": func(c *Config) { c.RemoteRegistryDir = "" }, "no forward": func(c *Config) { c.RemoteForwardDir = "" }, "no timeout": func(c *Config) { c.RemoteTimeout = 0 }, "relative": func(c *Config) { c.RemoteRegistryDir = "relative" }, "same path": func(c *Config) { c.RemoteForwardDir = c.RemoteRegistryDir }, "disabled with settings": func(c *Config) { c.RemoteEnabled = false }} {
		t.Run(name, func(t *testing.T) {
			c := good()
			mutate(c)
			if c.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
