package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/secretbindings"
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

func normalizeDataDir(cfg *config.Config) error {
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("invalid data directory")
	}
	cfg.DataDir = abs
	return nil
}

func effectiveEngineHost(cfg *config.Config) string {
	host := cfg.EngineSocket
	if host == "" {
		host = engine.DefaultHost(cfg.Engine)
	}
	if filepath.IsAbs(host) {
		host = "unix://" + host
	}
	return host
}

func run() error {
	cfg := config.Default()

	flag.StringVar(&cfg.Profile, "profile", cfg.Profile, "execution profile: legacy (compatibility) or alpha (explicit local target; mutations gated)")
	flag.StringVar(&cfg.Port, "port", cfg.Port, "HTTP listen port")
	flag.StringVar(&cfg.Bind, "bind", cfg.Bind, "HTTP listen IP (default loopback)")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for storing stack configs")
	flag.StringVar(&cfg.Engine, "engine", cfg.Engine, "container engine: docker or podman")
	flag.StringVar(&cfg.EngineSocket, "engine-socket", cfg.EngineSocket, "container engine socket/host, e.g. unix:///var/run/docker.sock")
	flag.StringVar(&cfg.DillBin, "dill", cfg.DillBin, "path to dill binary")
	flag.StringVar(&cfg.SecretTarget, "secret-target", "", "operator-selected Alpha target ID (enables strict secret mode with --secret-policy)")
	flag.StringVar(&cfg.SecretPolicy, "secret-policy", "", "absolute path to operator-controlled Alpha secret policy")
	flag.StringVar(&cfg.SecretPkl, "secret-pkl", "", "optional absolute path to operator-installed Pkl for Alpha secret mode")
	flag.BoolVar(&cfg.Dev, "dev", false, "dev mode: serve templates and static files from the repository (run from repo root)")

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

	if err := normalizeDataDir(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	log.Printf("warning: container engine socket access can grant host-level privileges; use an isolated target")

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
	effectiveHost := effectiveEngineHost(cfg)
	switch cfg.Engine {
	case "docker", "podman":
		host := effectiveHost
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
	runner.PklBinary = cfg.SecretPkl
	runner.Profile = cfg.Profile
	runner.Engine = cfg.Engine
	runner.Socket = strings.TrimPrefix(effectiveHost, "unix://")
	runner.ResolveStack = stackPathResolver(st)
	runner.CheckSource = stackSourceCheck(st)

	// Wire up local agent. Persisted restrictions continue to apply even when
	// secret support is not configured for new operations.
	ag := agent.NewLocalAgent(eng, runner)
	secretState, err := openSecretState(cfg.DataDir, cfg.SecretTarget != "")
	if err != nil {
		return fmt.Errorf("secret state unavailable")
	}
	if secretState != nil {
		defer secretState.Close()
		ag.SetSecretRecordLookup(func(stack string) (bool, error) {
			return secretState.SecretEnabled(secretbindings.DeploymentID(stack))
		})
	}
	runner.BeforeLegacyAction = legacySecretGate(st, eng, secretState)
	if secretState != nil {
		runner.BeforeLegacyValidate = legacySecretRecordGate(st, secretState)
	}
	if cfg.SecretTarget != "" {
		secretRunner := &dill.SecretRunner{Runner: runner, State: secretState, Coordinator: &runner.Coordinator, Engine: cfg.Engine, Socket: strings.TrimPrefix(cfg.EngineSocket, "unix://"), Policy: cfg.SecretPolicy, Target: cfg.SecretTarget, PklBinary: cfg.SecretPkl}
		runner.StrictActions = &dill.StrictController{Secret: secretRunner, Resolve: stackPathResolver(st), LegacyGate: runner.BeforeLegacyAction}
	}

	// Initialise git runner.
	gr := git.New()

	// Start server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, ag, st, rs, gr, authn)
	return srv.Run(ctx)
}

// openSecretState keeps known-secret restrictions even if strict mode is later
// disabled. Existing unsafe state is never silently ignored.
func openSecretState(dataDir string, create bool) (*secretbindings.Store, error) {
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(data, "secret-state")
	_, err = os.Lstat(root)
	if os.IsNotExist(err) {
		if !create {
			return nil, nil
		}
		if err := os.Mkdir(root, 0700); err != nil {
			return nil, err
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return secretbindings.Open(root, filepath.Join(data, "stacks"))
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
		log.Printf("  read credentials from the private file; they are not printed in logs")
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
