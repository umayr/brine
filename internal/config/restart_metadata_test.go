package config

import (
	"errors"
	"strings"
	"testing"
)

func metadataSettingsFixture() *Config {
	return &Config{RemoteEnabled: true, DataDir: "/not-provisioned/workspace", RemoteRegistryDir: "/not-provisioned/registry", RestartMetadata: RestartMetadataSettings{Enabled: true, ArchiveDir: "/not-provisioned/archive", KeyFile: "/not-provisioned/keys/key", StoreID: strings.Repeat("ab", 32), MaxBytes: 1024, MaxOperations: 10, Enrollments: []string{"host=" + strings.Repeat("cd", 16)}}}
}

func TestRestartMetadataStartupSyntaxOnly(t *testing.T) {
	c := metadataSettingsFixture()
	o, pins, err := c.RestartMetadataStartup()
	if err != nil || o.ArchiveDir != c.RestartMetadata.ArchiveDir || len(pins) != 1 || pins[0].Host != "host" || pins[0].Incarnation[0] != 0xcd {
		t.Fatalf("invalid parsed settings: %v", err)
	}
	c.RestartMetadata.Enrollments[0] = "replaced"
	if pins[0].Host != "host" {
		t.Fatal("startup pins alias input")
	}
	// Neither valid syntax nor disabled settings inspect any supplied path.
	disabled := &Config{DataDir: "\x00", RemoteRegistryDir: "\x00"}
	o, pins, err = disabled.RestartMetadataStartup()
	if err != nil || o.ArchiveDir != "" || len(pins) != 0 {
		t.Fatal("default-off inspected unrelated settings")
	}
}

func TestRestartMetadataStartupRefusesPartialOrAmbiguousSettings(t *testing.T) {
	cases := map[string]func(*Config){
		"disabled leftovers": func(c *Config) { c.RestartMetadata.Enabled = false },
		"no remote":          func(c *Config) { c.RemoteEnabled = false },
		"no auth":            func(c *Config) { c.NoAuth = true },
		"no identity":        func(c *Config) { c.RestartMetadata.StoreID = "" },
		"zero identity":      func(c *Config) { c.RestartMetadata.StoreID = strings.Repeat("0", 64) },
		"uppercase identity": func(c *Config) { c.RestartMetadata.StoreID = strings.Repeat("AB", 32) },
		"no capacity":        func(c *Config) { c.RestartMetadata.MaxBytes = 0 },
		"too many objects":   func(c *Config) { c.RestartMetadata.MaxOperations = 4096 },
		"no pins":            func(c *Config) { c.RestartMetadata.Enrollments = nil },
		"name only":          func(c *Config) { c.RestartMetadata.Enrollments = []string{"host"} },
		"zero pin":           func(c *Config) { c.RestartMetadata.Enrollments = []string{"host=" + strings.Repeat("0", 32)} },
		"duplicate": func(c *Config) {
			c.RestartMetadata.Enrollments = append(c.RestartMetadata.Enrollments, c.RestartMetadata.Enrollments[0])
		},
		"partial list":         func(c *Config) { c.RestartMetadata.Enrollments = append(c.RestartMetadata.Enrollments, "bad") },
		"archive in workspace": func(c *Config) { c.RestartMetadata.ArchiveDir = c.DataDir + "/archive" },
		"key in archive":       func(c *Config) { c.RestartMetadata.KeyFile = c.RestartMetadata.ArchiveDir + "/key" },
		"history overlap":      func(c *Config) { c.MetricsHistoryDir = c.RestartMetadata.ArchiveDir },
		"relative":             func(c *Config) { c.RestartMetadata.ArchiveDir = "relative" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := metadataSettingsFixture()
			mutate(c)
			o, pins, err := c.RestartMetadataStartup()
			if !errors.Is(err, ErrRestartMetadataSettings) || o.ArchiveDir != "" || pins != nil {
				t.Fatal("partially accepted invalid settings")
			}
		})
	}
}
