package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/umayr/brine/internal/remoteui"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/server"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
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
	flag.StringVar(&cfg.DillBin, "dill", cfg.DillBin, "operator-selected Dill binary for local execution")
	flag.StringVar(&cfg.StagingSocket, "staging-socket", "", "opt-in local dill-serve Unix socket")
	flag.StringVar(&cfg.StagingIdentity, "staging-identity", "", "stable operator-asserted staging target identifier")
	flag.StringVar(&cfg.OperationStateDir, "operation-state-dir", "", "S1 opt-in: preexisting canonical private0700 neutral request history outside data and coordination state")
	flag.StringVar(&cfg.CoordinationStateDir, "coordination-state-dir", "", "preexisting canonical shared owner-only0700 state outside Brine data (no automatic provisioning)")
	flag.StringVar(&cfg.CoordinationLayout, "coordination-layout", "", "explicit operator assertion: native-local-docker-v1 only")
	flag.StringVar(&cfg.CoordinationDaemonID, "coordination-daemon-id", "", "independently provisioned expected Docker daemon ID; never discovered from the selected socket")
	flag.StringVar(&cfg.SecretTarget, "secret-target", "", "operator-selected Alpha target ID (enables strict secret mode with --secret-policy)")
	flag.StringVar(&cfg.SecretPolicy, "secret-policy", "", "absolute path to operator-controlled Alpha secret policy")
	flag.StringVar(&cfg.SecretPkl, "secret-pkl", "", "optional absolute path to operator-installed Pkl for Alpha secret mode")
	flag.BoolVar(&cfg.Dev, "dev", false, "dev mode: serve templates and static files from the repository (run from repo root)")
	flag.BoolVar(&cfg.RemoteEnabled, "remote-enabled", false, "enable authenticated remote read-only UI")
	flag.StringVar(&cfg.RemoteRegistryDir, "remote-registry-dir", "", "absolute private remote registry directory")
	flag.StringVar(&cfg.RemoteDeployCredentials, "remote-deploy-credentials", "", "absolute private external target-bound deployment credential index")
	flag.StringVar(&cfg.RemoteForwardDir, "remote-forward-dir", "", "existing private directory for remote Unix socket forwards")
	flag.DurationVar(&cfg.RemoteTimeout, "remote-timeout", 0, "explicit remote connection and transport timeout")
	flag.BoolVar(&cfg.RemoteRevealEnabled, "remote-reveal-enabled", false, "enable audited remote reveal (requires provisioned seed and initialized audit)")
	flag.StringVar(&cfg.RemoteRevealSeedPath, "remote-reveal-seed-path", "", "absolute path to operator-provisioned reveal signing seed")
	flag.StringVar(&cfg.RemoteRevealAuditDir, "remote-reveal-audit-dir", "", "absolute path to pre-initialized private reveal audit directory")
	remoteDeployBinding := flag.String("remote-deploy-binding", "", "print non-secret deployment custody bindings for an existing HTTPS target and exit")
	initRemoteRegistry := flag.Bool("init-remote-registry", false, "initialize a new remote registry in the existing private directory and exit")
	flag.StringVar(&cfg.SourceHistoryDir, "source-history-dir", "", "pre-initialized private source-revision archive outside Brine data (opt-in)")
	flag.StringVar(&cfg.SourceHistoryKey, "source-history-key", "", "separately provisioned private 32-byte source archive key file")
	flag.IntVar(&cfg.SourceHistoryLimits.Objects, "source-history-max-objects", 0, fmt.Sprintf("maximum source archive objects across all stacks (0: %d, maximum %d)", snapshothistory.DefaultSourceArchiveObjects, snapshothistory.MaxSourceArchiveObjects))
	flag.Int64Var(&cfg.SourceHistoryLimits.Bytes, "source-history-max-bytes", 0, fmt.Sprintf("maximum logical sealed source archive bytes (0: %d MiB, maximum %d MiB)", snapshothistory.DefaultSourceArchiveBytes>>20, snapshothistory.MaxSourceArchiveBytes>>20))
	initSourceHistory := flag.Bool("init-source-history", false, "initialize an empty, externally provisioned source archive and exit; never generate keys")
	registerMetricsHistoryFlags(flag.CommandLine, cfg)
	initWebhooks := flag.Bool("init-webhooks", false, "initialize new webhook state using a pre-provisioned external key, then exit; never repair missing state")
	flag.BoolVar(&cfg.WebhooksEnabled, "webhooks-enabled", false, "enable authenticated webhook management and signed event delivery")
	flag.StringVar(&cfg.WebhooksDir, "webhooks-dir", "", "absolute private directory for durable webhook state")
	flag.StringVar(&cfg.WebhooksKeyFile, "webhooks-key-file", "", "separately provisioned private file containing exactly 32 raw key bytes, outside Brine data")
	flag.IntVar(&cfg.WebhooksMaxPending, "webhooks-max-pending", 0, "maximum pending webhook deliveries (0: 10000, maximum 100000)")
	flag.DurationVar(&cfg.WebhooksRetention, "webhooks-retention", 0, "webhook event/delivery retention (0: 168h; range 24h–2160h)")
	flag.Func("webhooks-allow-private-cidr", "explicit private receiver subnet; repeatable; metadata, loopback and link-local remain forbidden", func(value string) error {
		cfg.WebhooksAllowPrivateCIDRs = append(cfg.WebhooksAllowPrivateCIDRs, value)
		return nil
	})

	flag.StringVar(&cfg.AuthUser, "auth-user", cfg.AuthUser, "admin username (default: stored or admin)")
	flag.StringVar(&cfg.AuthPassword, "auth-password", cfg.AuthPassword, "admin password (default: generated and persisted)")
	flag.StringVar(&cfg.APIToken, "api-token", cfg.APIToken, "bearer token for API clients (default: generated and persisted)")
	flag.BoolVar(&cfg.NoAuth, "no-auth", false, "disable authentication (local development only)")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", false, "mark session cookies Secure (enable when serving over TLS)")
	flag.DurationVar(&cfg.SessionTTL, "session-ttl", cfg.SessionTTL, "login session lifetime")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "timeout for ordinary HTTP handlers")
	flag.DurationVar(&cfg.DeployTimeout, "deploy-timeout", cfg.DeployTimeout, "timeout for deploy/teardown/git commands")

	quarantineAction := registerQuarantineFlags(flag.CommandLine, &cfg.DataDir, os.Stdout)
	ver := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *ver {
		fmt.Println(version)
		return nil
	}

	if *initWebhooks {
		if err := normalizeDataDir(cfg); err != nil {
			return err
		}
		return server.InitializeWebhooks(cfg)
	}
	if *initSourceHistory {
		if err := normalizeDataDir(cfg); err != nil {
			return err
		}
		if cfg.SourceHistoryDir == "" || cfg.SourceHistoryKey == "" {
			return fmt.Errorf("explicit source archive and key required")
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		return snapshothistory.InitializeSourceArchive(cfg.SourceHistoryDir, cfg.SourceHistoryKey, cfg.DataDir)
	}
	if handled, err := quarantineAction(); handled {
		return err
	}

	if *remoteDeployBinding != "" {
		registry, e := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
		if e != nil {
			return fmt.Errorf("remote registry unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		binding, e := remoteui.DeploymentBindingMetadata(ctx, registry, *remoteDeployBinding)
		if e != nil {
			return fmt.Errorf("deployment custody binding unavailable")
		}
		return json.NewEncoder(os.Stdout).Encode(binding)
	}
	if *initRemoteRegistry {
		// Initialization is an explicit local operation; it never starts the server.
		if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
			return fmt.Errorf("initialize remote registry: %w", err)
		}
		return nil
	}

	var emptyCoordinationOption, disabledOption bool
	flag.Visit(func(f *flag.Flag) {
		if strings.HasPrefix(f.Name, "secret-") || strings.HasPrefix(f.Name, "coordination-") || f.Name == "operation-state-dir" {
			disabledOption = true
		}
		if (strings.HasPrefix(f.Name, "coordination-") || f.Name == "operation-state-dir" || strings.HasPrefix(f.Name, "staging-") || f.Name == "remote-reveal-seed-path" || f.Name == "remote-reveal-audit-dir") && f.Value.String() == "" {
			emptyCoordinationOption = true
		}
	})
	if disabledOption {
		return fmt.Errorf("Alpha and strict secret options are disabled")
	}
	if emptyCoordinationOption {
		return fmt.Errorf("coordination options must not be explicitly empty")
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
	quarantine, err := secretbindings.NewQuarantine(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("local process quarantine unavailable: %w", err)
	}
	runner.ConfigureQuarantine(quarantine)
	variableStore, err := configvars.New(cfg.DataDir, st)
	if err != nil {
		return fmt.Errorf("init configuration variables: %w", err)
	}
	runner.Variables = variableStore
	if cfg.QualifiedOperations() {
		runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: cfg.CoordinationStateDir, Layout: cfg.CoordinationLayout, DaemonID: cfg.CoordinationDaemonID}
	}

	// Wire up local agent. Persisted restrictions continue to apply even when
	// secret support is not configured for new operations.
	ag := agent.NewLocalAgent(eng, runner)
	secretState, err := openSecretState(cfg.DataDir, cfg.SecretTarget != "")
	if err != nil {
		return fmt.Errorf("secret state unavailable")
	}
	if cfg.SourceHistoryDir != "" {
		if secretState == nil {
			return fmt.Errorf("source history requires the existing private protection-state directory; verify it offline")
		}
		runner.CheckSourceProtection = func(name string) error {
			return secretState.RequireNoSourceProtection(secretbindings.DeploymentID(name), filepath.Join(cfg.DataDir, "stacks"))
		}
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

	authn, created, path, err := auth.NewWithCredentials(auth.Config{
		User:          cfg.AuthUser,
		Password:      cfg.AuthPassword,
		APIToken:      cfg.APIToken,
		SecureCookies: cfg.SecureCookies,
		SessionTTL:    cfg.SessionTTL,
	}, cfg.DataDir)
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

	return authn, nil
}
