package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	Port    string
	DataDir string
	Engine  string // "docker" | "podman"
	DillBin string // path to dill binary
	Dev     bool   // hot-reload templates in dev mode
}

func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port:    "8080",
		DataDir: filepath.Join(home, ".brine"),
		Engine:  "docker",
		DillBin: "dill",
	}
}
