package server

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
	"github.com/umayr/brine/internal/web"
)

// Server is the brine HTTP server.
type Server struct {
	cfg               *config.Config
	agent             agent.Agent
	store             store.StackStore
	recipes           store.RecipeStore
	git               *git.Runner
	router            http.Handler
	web               *web.Handler
	authn             *auth.Authenticator
	remoteCancel      context.CancelFunc
	remoteDeploy      *remoteui.Deployment
	testRevealFactory func(*auth.Authenticator, *targetregistry.Registry) (*remoteui.RevealHandler, error) // synthetic tests only
}

// New wires up all dependencies and returns a ready-to-run server.
func New(cfg *config.Config, ag agent.Agent, st store.StackStore, rs store.RecipeStore, gr *git.Runner, authn *auth.Authenticator) *Server {
	r := chi.NewRouter()
	// Do not trust client-supplied forwarding headers for login rate limiting.
	// Do not log request URLs: query strings may contain sensitive data.
	r.Use(middleware.Recoverer)
	r.Use(middleware.CleanPath)

	// Authentication guards every route except the login page, static assets,
	// and the health endpoint.
	r.Use(authn.Middleware)

	// Login is public; logout requires authentication and CSRF for sessions.
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
	if cfg.StagingSocket != "" && cfg.StagingIdentity != "" {
		h.EnableLocalStaging(cfg)
	}
	web.Mount(r, h, timeouts)

	return &Server{cfg: cfg, agent: ag, store: st, recipes: rs, git: gr, router: r, web: h, authn: authn}
}

// Run starts HTTP and any configured remote Git worker until ctx is cancelled.
func (s *Server) Run(ctx context.Context) (runErr error) {
	defer s.web.CloseStaging()
	defer func() {
		if s.remoteDeploy != nil {
			s.remoteDeploy.Close()
		}
	}()
	if err := s.cfg.Validate(); err != nil {
		return err
	}
	if s.cfg.SourceHistoryDir != "" {
		archive, err := snapshothistory.OpenSourceArchive(s.cfg.SourceHistoryDir, s.cfg.SourceHistoryKey, s.cfg.DataDir, s.cfg.SourceHistoryLimits)
		if err != nil {
			return fmt.Errorf("open source history: %w", err)
		}
		defer archive.Close()
		if err = s.web.EnableSourceHistory(archive); err != nil {
			return err
		}
	}
	if s.cfg.RemoteEnabled {
		if err := s.prepareRemote(nil, nil); err != nil {
			return err
		}
	}
	if s.remoteCancel != nil {
		defer s.remoteCancel()
	}
	history := beginOptionalMetricsHistory(ctx, s.cfg)
	defer func() {
		if err := history.stop(5 * time.Second); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	// Do not start background mutations until the configuration is validated.
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	// Local Git auto-deploy is paused until it can apply a reviewed candidate.
	if s.remoteDeploy != nil {
		go s.remoteDeploy.RunGit(workerCtx)
	}
	addr := net.JoinHostPort(s.cfg.Bind, s.cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // disabled for SSE streams
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("brine listening on http://%s\n", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		history.cutoff()
		// Cancel snapshot transfers before the HTTP shutdown grace period.
		s.web.CloseStaging()
		if s.remoteCancel != nil {
			s.remoteCancel()
		}
		return shutdownHTTP(srv, 10*time.Second)
	}
}

func shutdownHTTP(srv *http.Server, grace time.Duration) error {
	shutCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		// Report the failed drain and force-close lingering connections.
		return errors.Join(err, srv.Close())
	}
	return nil
}
