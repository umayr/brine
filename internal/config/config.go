package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	Port         string
	Bind         string // literal listen address
	DataDir      string
	Engine       string // "docker" | "podman"
	EngineSocket string // Docker-compatible engine host/socket, e.g. unix:///var/run/docker.sock
	DillBin      string // path to dill binary
	Dev          bool   // hot-reload templates in dev mode

	// Authentication. Password/APIToken may be empty, in which case
	// credentials are loaded from {DataDir}/credentials.json or generated.
	AuthUser      string
	AuthPassword  string
	APIToken      string
	NoAuth        bool
	SecureCookies bool

	SessionTTL     time.Duration
	RequestTimeout time.Duration // ordinary HTTP handlers
	DeployTimeout  time.Duration // dill/git commands
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port:    "8080",
		Bind:    "127.0.0.1",
		DataDir: filepath.Join(home, ".brine"),
		Engine:  "docker",
		DillBin: "dill",

		AuthUser:     envOr("BRINE_AUTH_USER", "admin"),
		AuthPassword: os.Getenv("BRINE_AUTH_PASSWORD"),
		APIToken:     os.Getenv("BRINE_API_TOKEN"),

		SessionTTL:     12 * time.Hour,
		RequestTimeout: 30 * time.Second,
		DeployTimeout:  30 * time.Minute,
	}
}

// Validate rejects ambiguous binds and prevents unauthenticated non-loopback serving.
func (c *Config) Validate() error {
	ip := net.ParseIP(c.Bind)
	if ip == nil {
		return fmt.Errorf("bind must be a literal IP address")
	}
	if c.NoAuth && !ip.IsLoopback() {
		return fmt.Errorf("--no-auth requires a loopback bind")
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
