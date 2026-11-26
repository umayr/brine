package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/targetregistry"
)

// prepareRemote runs only after validated opt-in. The optional dependencies
// permit synthetic end-to-end tests without connecting to a host or engine.
func (s *Server) prepareRemote(provider remoteui.ReadLeaseProvider, observer remoteui.EnrollmentObserver) error {
	if !s.cfg.RemoteEnabled || s.authn == nil || !s.authn.Enabled() {
		return errors.New("remote requires explicit enablement and authentication")
	}
	if s.cfg.RemoteForwardDir != "" {
		if err := agenttransport.ValidateLocalDir(s.cfg.RemoteForwardDir); err != nil {
			return errors.New("remote forwarding directory unavailable or insecure")
		}
	}
	registry, err := targetregistry.OpenExisting(s.cfg.RemoteRegistryDir)
	if err != nil {
		return errors.New("remote registry unavailable or insecure")
	}
	transport := remoteagent.Config{LocalDir: s.cfg.RemoteForwardDir, Timeout: s.cfg.RemoteTimeout}
	s.targetRegistry = registry
	if provider == nil {
		provider, err = remoteui.NewRegistryLeaseProvider(registry, transport)
		if err != nil {
			return err
		}
	}
	if observer == nil {
		observer = remoteui.PinnedObserver{Config: transport}
	}
	backend, err := remoteui.NewReadBackend(registry, provider)
	if err != nil {
		return err
	}
	reads, err := remoteui.New(backend, s.authn)
	if err != nil {
		return err
	}
	enrollment, err := remoteui.NewEnrollmentHandler(s.authn, registry, observer)
	if err != nil {
		return err
	}
	var reveals []*remoteui.RevealHandler
	if s.cfg.RemoteRevealEnabled {
		reveal, err := remoteui.OpenRevealHandler(context.Background(), s.authn, registry, s.cfg.RemoteRevealAuditDir, s.cfg.RemoteRevealSeedPath, transport)
		if err != nil {
			return errors.New("remote reveal configuration unavailable or insecure")
		}
		if s.testRevealFactory != nil {
			reveal, err = s.testRevealFactory(s.authn, registry)
			if err != nil {
				return errors.New("remote reveal test handler unavailable")
			}
		}
		reveals = append(reveals, reveal)
	}
	remote, err := remoteui.ProtectedRoutes(reads, enrollment, reveals...)
	if err != nil {
		return err
	}
	var deploy http.Handler
	if s.store != nil {
		if s.remoteDeploy, err = remoteui.NewDeployment(s.authn, registry, s.store); err != nil {
			return err
		}
		s.remoteDeploy.EnableGit(s.git)
		deploy = s.remoteDeploy.Protected()
	}
	local := s.router
	lifetime, cancel := context.WithCancel(context.Background())
	s.remoteCancel = cancel
	s.router = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote" || strings.HasPrefix(r.URL.Path, "/remote/") {
			if lifetime.Err() != nil {
				http.Error(w, "Remote access shutting down", http.StatusServiceUnavailable)
				return
			}
			ctx, cancelRequest := context.WithCancel(r.Context())
			stop := context.AfterFunc(lifetime, cancelRequest)
			defer func() { stop(); cancelRequest() }()
			if deploy != nil && remoteui.IsDeploymentPath(r.URL.Path) {
				deploy.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			remote.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		local.ServeHTTP(w, r)
	})
	s.web.EnableRemoteNavigation()
	return nil
}
