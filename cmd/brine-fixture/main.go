// Command brine-fixture serves disposable, read-only UI fixtures on localhost.
// It never connects to a container engine or executes Dill.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/errdefs"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

func main() {
	port := flag.String("port", "18082", "localhost port")
	withAuth := flag.Bool("auth", false, "enable disposable admin login (fixture-only-not-a-secret)")
	syntheticStaging := flag.Bool("synthetic-staging", false, "enable disposable fake local snapshot staging fixture")
	syntheticWebhooks := flag.Bool("synthetic-webhooks", false, "enable disposable webhook management fixture; never sends HTTP or runs actions")
	syntheticSecrets := flag.Bool("synthetic-secrets", false, "enable reference-only broker UI fixtures; requires --auth; never acquires values or deploys")
	flag.Parse()
	if *syntheticSecrets && !*withAuth {
		log.Fatal("synthetic secrets requires --auth")
	}
	dir, err := os.MkdirTemp("", "brine-ui-fixture-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.NewFSStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	rs, err := store.NewFSRecipeStore(dir)
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range []string{"observability", "media", "homepage"} {
		pkl := fmt.Sprintf("amends \"package://pkg.ushd.ca/dill@0.4.0#/dill.pkl\"\n\nconfig {\n  name = %q\n  engine = \"docker\"\n}\n", name)
		if _, err := st.Create(name, pkl); err != nil {
			log.Fatal(err)
		}
	}
	// Existing local nested module for editor browsing checks.
	moduleDir := filepath.Join(dir, "stacks", "observability", "modules")
	if err := os.Mkdir(moduleDir, 0700); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "settings & local.pkl"), []byte("value = 1\n"), 0600); err != nil {
		log.Fatal(err)
	}
	// Exercise receiver-incompatible filenames through the real preview handler.
	if err := os.WriteFile(filepath.Join(dir, "stacks", "media", "<unsafe>.txt"), []byte("fixture"), 0600); err != nil {
		log.Fatal(err)
	}
	fixtureStacks := &fixtureStore{StackStore: st, LocalEditorWorkspace: st}
	eng := &fixtureEngine{}
	// Serve one populated stack inventory through the real read-only handler.
	// The stub cannot invoke Dill or contact an engine; all other invocations fail.
	homepage, err := st.Get("homepage")
	if err != nil {
		log.Fatal(err)
	}
	stub := filepath.Join(dir, "fixture-dill")
	if err := os.WriteFile(stub, []byte(`#!/bin/sh
[ "$4" = homepage ] && [ "${9}" = ps ] || exit 1
printf '%s\n' '[{"id":"fixture-web","name":"fixture-web","state":"running"},{"id":"fixture-worker","name":"fixture-worker","state":"exited"}]'
`), 0700); err != nil {
		log.Fatal(err)
	}
	runner := dill.New(stub)
	runner.Engine = "docker"
	runner.Socket = filepath.Join(dir, "unused.sock")
	runner.ResolveStack = func(path string) (string, error) {
		if path == homepage.PklPath {
			return "homepage", nil
		}
		return "", fmt.Errorf("no synthetic inventory for this stack")
	}
	variables, err := configvars.New(dir, fixtureStacks)
	if err != nil {
		log.Fatal(err)
	}
	for _, item := range []struct{ stack, name, value string }{
		{"", "APP_DOMAIN", "example.test"}, {"", "TIMEZONE", "UTC"},
		{"homepage", "APP_DOMAIN", "homepage.example.test"},
	} {
		if err := variables.Set(item.stack, item.name, item.value, false); err != nil {
			log.Fatal(err)
		}
	}
	runner.Variables = variables
	if *syntheticStaging || *syntheticWebhooks {
		*withAuth = true
	}
	ag := agent.NewLocalAgent(eng, runner)
	gr := git.New()
	authn, err := auth.New(auth.Config{NoAuth: !*withAuth, User: "admin", Password: "fixture-only-not-a-secret"})
	if err != nil {
		log.Fatal(err)
	}
	r := chi.NewRouter()
	var stagingMu sync.Mutex
	var stagingBodies []json.RawMessage
	var stagingSocket string
	if *syntheticStaging {
		stagingSocket = filepath.Join(dir, "staging.sock")
		listener, err := net.Listen("unix", stagingSocket)
		if err != nil {
			log.Fatal(err)
		}
		fake := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/health":
				io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
			case "/v1/status":
				io.WriteString(w, `{"stack":"homepage","engine":"docker","containers":[]}`)
			case "/v1/snapshots":
				b, _ := io.ReadAll(r.Body)
				stagingMu.Lock()
				stagingBodies = append(stagingBodies, b)
				stagingMu.Unlock()
				w.WriteHeader(201)
				io.WriteString(w, `{"status":"staged","stage_id":"0123456789abcdef0123456789abcdef"}`)
			default:
				http.NotFound(w, r)
			}
		})}
		go fake.Serve(listener)
		defer fake.Close()
	}
	// Allow local login and fake snapshot staging, but never lifecycle mutations.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			loginOnly := *withAuth && r.Method == http.MethodPost && (r.URL.Path == "/login" || r.URL.Path == "/logout")
			stagingOnly := *syntheticStaging && r.Method == http.MethodPost && (r.URL.Path == "/web/stacks/homepage/staging/prepare" || r.URL.Path == "/web/stacks/homepage/staging/confirm")
			hooksOnly := *syntheticWebhooks && strings.HasPrefix(r.URL.Path, "/api/webhooks/")
			referencesOnly := *syntheticSecrets && r.Method == http.MethodPost && r.URL.Path == "/stacks/homepage/secrets"
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !loginOnly && !stagingOnly && !referencesOnly && !hooksOnly {
				http.Error(w, "fixture is read-only", http.StatusMethodNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Use(authn.Middleware)
	if *syntheticStaging {
		r.Get("/fixture-staging-audit", func(w http.ResponseWriter, r *http.Request) {
			stagingMu.Lock()
			defer stagingMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stagingBodies)
		})
	}
	r.Get("/login", authn.HandleLogin)
	r.Post("/login", authn.HandleLogin)
	r.Post("/logout", authn.HandleLogout)
	timeouts := httpx.Timeouts{Default: 30 * time.Second, Lifecycle: 30 * time.Second}
	webHandler := web.New(ag, fixtureStacks, rs, gr, "docker", true, authn, 30*time.Second)
	if *syntheticSecrets {
		if err := brokerapp.Initialize(dir); err != nil {
			log.Fatal(err)
		}
		scope := secretbroker.Scope{RegistrationID: strings.Repeat("a", 32), StackName: "homepage", TargetRegistration: "local"}
		policy := brokerapp.Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{
			"vault":     {Vault: &secrets.VaultConfig{Endpoint: "https://vault.example.invalid", Mount: "secret", PathPrefix: "apps", TokenFile: secrets.FileRef{Path: "/operator/private-vault-token"}}},
			"infisical": {Infisical: &secrets.InfisicalConfig{Endpoint: "https://infisical.example.invalid", ProjectID: "demo", Environment: "production", Folder: "/apps", ClientIDFile: secrets.FileRef{Path: "/operator/private-client-id"}, ClientSecretFile: secrets.FileRef{Path: "/operator/private-client-secret"}}},
			"files":     {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"database-password": {Path: "/operator/private-file"}}}},
		}}, Registrations: []brokerapp.Registration{{Scope: scope, Connections: []string{"vault", "infisical", "files"}}}}
		raw, err := json.Marshal(policy)
		if err != nil {
			log.Fatal(err)
		}
		path := filepath.Join(dir, "broker-policy.json")
		if os.WriteFile(path, raw, 0600) != nil {
			log.Fatal("fixture policy unavailable")
		}
		service, err := brokerapp.Open(dir, path)
		if err != nil {
			log.Fatal(err)
		}
		defer service.Close()
		webHandler.EnableBroker(service)
	}
	if *syntheticStaging {
		cfg := config.Default()
		cfg.StagingSocket = stagingSocket
		cfg.StagingIdentity = "fixture-target"
		webHandler.EnableLocalStaging(cfg)
		defer webHandler.CloseStaging()
	}
	if *syntheticWebhooks {
		svc, err := enableFixtureWebhooks(webHandler, dir)
		if err != nil {
			log.Fatal(err)
		}
		defer svc.Close()
	}
	api.Mount(r, ag, fixtureStacks, rs, gr, "docker", timeouts, 30*time.Second)
	web.Mount(r, webHandler, timeouts)
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", *port))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("read-only UI fixtures at http://%s (temporary data: %s)", listener.Addr(), dir)
	server := &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = server.Close() }()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
}

type fixtureStore struct {
	store.StackStore
	store.LocalEditorWorkspace
}

func (*fixtureStore) timestamp(stack *store.Stack) *store.Stack {
	stack.CreatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	stack.UpdatedAt = stack.CreatedAt
	return stack
}
func (s *fixtureStore) Get(name string) (*store.Stack, error) {
	stack, err := s.StackStore.Get(name)
	if err != nil {
		return nil, err
	}
	// A synthetic Git-backed view exercises Git panel controls without a remote.
	if name == "media" {
		stack.Git = &store.GitSource{URL: "https://example.invalid/fixture.git", Branch: "main"}
	}
	return s.timestamp(stack), nil
}
func (s *fixtureStore) List() ([]*store.Stack, error) {
	stacks, err := s.StackStore.List()
	for _, stack := range stacks {
		s.timestamp(stack)
	}
	return stacks, err
}

type fixtureEngine struct{}

var containers = []engine.ContainerInfo{
	{ID: "a1111111111111111111111111111111", Name: "grafana", Image: "grafana/grafana:11.4", State: "running", Status: "Up 3 days", Ports: []string{"3000:3000"}, Stack: "observability", Labels: map[string]string{"dill.stack": "observability"}},
	{ID: "b2222222222222222222222222222222", Name: "prometheus", Image: "prom/prometheus:v3.0", State: "running", Status: "Up 3 days", Ports: []string{"9090:9090"}, Stack: "observability", Labels: map[string]string{"dill.stack": "observability"}},
	{ID: "c3333333333333333333333333333333", Name: "jellyfin", Image: "jellyfin/jellyfin:10.10", State: "exited", Status: "Exited (0) 2 hours ago", Stack: "media", Labels: map[string]string{"dill.stack": "media"}},
	{ID: "d4444444444444444444444444444444", Name: "homepage", Image: "ghcr.io/gethomepage/homepage:latest", State: "running", Status: "Up 1 day", Ports: []string{"8080:3000"}, Stack: "homepage", Labels: map[string]string{"dill.stack": "homepage"}},
}

func (*fixtureEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	return containers, nil
}
func (*fixtureEngine) InspectContainer(_ context.Context, id string) (*engine.ContainerInfo, error) {
	for _, c := range containers {
		if c.ID == id || c.Name == id {
			return &c, nil
		}
	}
	return nil, errdefs.NotFound(fmt.Errorf("fixture container not found"))
}
func (*fixtureEngine) ListImages(context.Context) ([]engine.ImageInfo, error) {
	created := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	return []engine.ImageInfo{{ID: "sha256:aaaaaaaaaaaaaaaa", Tags: []string{"grafana/grafana:11.4"}, Size: 425000000, Created: created}, {ID: "sha256:bbbbbbbbbbbbbbbb", Tags: []string{"prom/prometheus:v3.0"}, Size: 285000000, Created: created}, {ID: "sha256:cccccccccccccccc", Tags: []string{"jellyfin/jellyfin:10.10"}, Size: 510000000, Created: created}}, nil
}
func (*fixtureEngine) ContainerLogs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("2025-01-15T12:00:00Z service started\n2025-01-15T12:00:01Z ready\n")), nil
}
func (*fixtureEngine) StartContainer(context.Context, string) error {
	return fmt.Errorf("fixture is read-only")
}
func (*fixtureEngine) StopContainer(context.Context, string) error {
	return fmt.Errorf("fixture is read-only")
}
func (*fixtureEngine) RestartContainer(context.Context, string) error {
	return fmt.Errorf("fixture is read-only")
}
func (*fixtureEngine) PullImage(context.Context, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("fixture is read-only")
}
func (*fixtureEngine) RemoveImage(context.Context, string, bool) error {
	return fmt.Errorf("fixture is read-only")
}
func (*fixtureEngine) Close() error { return nil }
