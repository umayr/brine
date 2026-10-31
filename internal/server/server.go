package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

// Server is the brine HTTP server.
type Server struct {
	cfg     *config.Config
	agent   agent.Agent
	store   store.StackStore
	recipes store.RecipeStore
	git     *git.Runner
	syncer  *git.Syncer
	router  http.Handler
}

// New wires up all dependencies and returns a ready-to-run server.
func New(cfg *config.Config, ag agent.Agent, st store.StackStore, rs store.RecipeStore, gr *git.Runner, authn *auth.Authenticator) *Server {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.CleanPath)

	// Authentication guards every route except the login page, static assets,
	// and the health endpoint.
	r.Use(authn.Middleware)

	// Login/logout are public paths handled before the authentication check.
	r.Get("/login", authn.HandleLogin)
	r.Post("/login", authn.HandleLogin)
	r.Post("/logout", authn.HandleLogout)

	timeouts := httpx.Timeouts{
		Default:   cfg.RequestTimeout,
		Lifecycle: cfg.DeployTimeout + time.Minute,
	}

	// API routes
	api.Mount(r, ag, st, rs, gr, cfg.Engine, timeouts, cfg.DeployTimeout)

	// Web / HTMX routes
	h := web.New(ag, st, rs, gr, cfg.Engine, cfg.Dev, authn, cfg.DeployTimeout)
	web.Mount(r, h, timeouts)

	syncer := git.NewSyncer(st, gr, ag.Dill(), cfg.DeployTimeout)

	return &Server{cfg: cfg, agent: ag, store: st, recipes: rs, git: gr, syncer: syncer, router: r}
}

// Run starts the HTTP server and the GitOps syncer, blocking until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Start the GitOps syncer in the background.
	go s.syncer.Run(ctx)

	addr := fmt.Sprintf(":%s", s.cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // disabled for SSE streams
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("brine listening on http://localhost%s\n", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}
