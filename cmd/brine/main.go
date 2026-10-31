package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
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
	flag.StringVar(&cfg.EngineSocket, "engine-socket", cfg.EngineSocket, "container engine socket/host, e.g. unix:///var/run/docker.sock")
	flag.StringVar(&cfg.DillBin, "dill", cfg.DillBin, "path to dill binary")
	flag.BoolVar(&cfg.Dev, "dev", false, "dev mode: re-parse templates on every request")

	flag.StringVar(&cfg.AuthUser, "auth-user", cfg.AuthUser, "admin username")
	flag.StringVar(&cfg.AuthPassword, "auth-password", cfg.AuthPassword, "admin password (default: generated and persisted)")
	flag.StringVar(&cfg.APIToken, "api-token", cfg.APIToken, "bearer token for API clients (default: generated and persisted)")
	flag.BoolVar(&cfg.NoAuth, "no-auth", false, "disable authentication (local development only)")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", false, "mark session cookies Secure (enable when serving over TLS)")
	flag.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "login session lifetime")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "timeout for ordinary HTTP handlers")
	flag.DurationVar(&cfg.DeployTimeout, "deploy-timeout", cfg.DeployTimeout, "timeout for deploy/teardown/git commands")

	ver := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *ver {
		fmt.Println(version)
		return nil
	}

	authn, err := buildAuth(cfg)
	if err != nil {
		return err
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
	case "docker", "podman":
		host := cfg.EngineSocket
		if host == "" {
			host = engine.DefaultHost(cfg.Engine)
		}
		eng, err = engine.NewDockerEngineWithHost(host)
		if err != nil {
			return fmt.Errorf("init %s engine (%s): %w", cfg.Engine, host, err)
		}
	default:
		return fmt.Errorf("unsupported engine %q (supported: docker, podman)", cfg.Engine)
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

	srv := server.New(cfg, ag, st, rs, gr, authn)
	return srv.Run(ctx)
}

// buildAuth resolves credentials and constructs the authenticator. Explicit
// flags/env win; otherwise credentials are loaded from (or created in) the
// data directory so restarts do not lock the operator out.
func buildAuth(cfg *config.Config) (*auth.Authenticator, error) {
	if cfg.NoAuth {
		log.Printf("warning: authentication disabled (--no-auth); do not expose this instance")
		return auth.New(auth.Config{NoAuth: true})
	}

	creds, created, path, err := auth.LoadOrCreate(cfg.DataDir, cfg.AuthUser, cfg.AuthPassword, cfg.APIToken)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials: %w", err)
	}
	if created {
		log.Printf("auth enabled: generated credentials at %s", path)
		log.Printf("  username:  %s", creds.User)
		log.Printf("  password:  %s", creds.Password)
		log.Printf("  api token: %s", creds.APIToken)
		log.Printf("  (store these now; the password is not printed again)")
	} else if path != "" {
		log.Printf("auth enabled: using credentials from %s", path)
	} else {
		log.Printf("auth enabled: using credentials from flags/env")
	}

	return auth.New(auth.Config{
		User:          creds.User,
		Password:      creds.Password,
		APIToken:      creds.APIToken,
		SecureCookies: cfg.SecureCookies,
		SessionTTL:    cfg.SessionTTL,
	})
}
