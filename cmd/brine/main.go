package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/server"
	"github.com/umayr/brine/internal/store"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Default()

	flag.StringVar(&cfg.Port, "port", cfg.Port, "HTTP listen port")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for storing stack configs")
	flag.StringVar(&cfg.Engine, "engine", cfg.Engine, "container engine: docker or podman")
	flag.StringVar(&cfg.DillBin, "dill", cfg.DillBin, "path to dill binary")
	flag.BoolVar(&cfg.Dev, "dev", false, "dev mode: re-parse templates on every request")
	ver := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *ver {
		fmt.Println(version)
		return nil
	}

	// Initialise stack store.
	st, err := store.NewFSStore(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}

	// Initialise recipe store.
	rs, err := store.NewFSRecipeStore(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("init recipe store: %w", err)
	}

	// Initialise container engine.
	var eng engine.Engine
	switch cfg.Engine {
	case "docker":
		eng, err = engine.NewDockerEngine()
		if err != nil {
			return fmt.Errorf("init docker engine: %w", err)
		}
	default:
		return fmt.Errorf("unsupported engine %q (supported: docker)", cfg.Engine)
	}
	defer eng.Close()

	// Initialise dill runner.
	runner := dill.New(cfg.DillBin)

	// Wire up local agent.
	ag := agent.NewLocalAgent(eng, runner)

	// Initialise git runner.
	gr := git.New()

	// Start server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, ag, st, rs, gr)
	return srv.Run(ctx)
}
