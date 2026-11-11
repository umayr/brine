// Command brine-fixture serves disposable, read-only UI fixtures on localhost.
// It never connects to a container engine or executes Dill.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

func main() {
	port := flag.String("port", "18082", "localhost port")
	withAuth := flag.Bool("auth", false, "enable disposable admin login (fixture-only-not-a-secret)")
	flag.Parse()
	dir, err := os.MkdirTemp("", "brine-ui-fixture-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
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
	fixtureStacks := &fixtureStore{StackStore: st}
	eng := &fixtureEngine{}
	ag := agent.NewLocalAgent(eng, dill.New("/nonexistent-brine-fixture-dill"))
	gr := git.New()
	authn, err := auth.New(auth.Config{NoAuth: !*withAuth, User: "admin", Password: "fixture-only-not-a-secret"})
	if err != nil {
		log.Fatal(err)
	}
	r := chi.NewRouter()
	// Fixture mode is read-only for infrastructure/API/HTMX. Optional auth
	// permits only local login/logout POSTs, never lifecycle mutations.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			loginOnly := *withAuth && r.Method == http.MethodPost && (r.URL.Path == "/login" || r.URL.Path == "/logout")
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !loginOnly {
				http.Error(w, "fixture is read-only", http.StatusMethodNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Use(authn.Middleware)
	r.Get("/login", authn.HandleLogin)
	r.Post("/login", authn.HandleLogin)
	r.Post("/logout", authn.HandleLogout)
	timeouts := httpx.Timeouts{Default: 30 * time.Second, Lifecycle: 30 * time.Second}
	api.Mount(r, ag, fixtureStacks, rs, gr, "docker", timeouts, 30*time.Second)
	web.Mount(r, web.New(ag, fixtureStacks, rs, gr, "docker", true, authn, 30*time.Second), timeouts)
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

type fixtureStore struct{ store.StackStore }

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
	return nil, fmt.Errorf("fixture container not found")
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
