package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/webhooks"
)

// Existing ancestors are resolved so a parent-directory alias cannot put the
// encryption key inside stack data or the encrypted database directory.
func webhookRealPath(path string) (string, error) {
	tail := []string{}
	for {
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", e
		}
		tail = append(tail, filepath.Base(path))
		path = parent
	}
}
func webhookWithin(path, root string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

// Only this exact HMAC-authenticated POST route is outside browser auth. Other
// paths/methods (including encoded/cleaned variants) keep ordinary auth+CSRF.
func webhookIngressPath(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || !strings.HasPrefix(r.URL.Path, "/hooks/trg_") {
		return false
	}
	id := strings.TrimPrefix(r.URL.Path, "/hooks/trg_")
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (s *Server) withWebhookIngress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.WebhooksEnabled && webhookIngressPath(r) {
			if s.webhooks == nil {
				http.Error(w, "Webhook service unavailable", 503)
				return
			}
			s.webhooks.Ingress(s.webhookDispatcher).ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loadWebhookKey(cfg *config.Config) ([]byte, error) {
	keyPath, e := webhookRealPath(cfg.WebhooksKeyFile)
	if e != nil {
		return nil, webhooks.ErrKey
	}
	dataPath, e := webhookRealPath(cfg.DataDir)
	if e != nil {
		return nil, webhooks.ErrKey
	}
	statePath, e := webhookRealPath(cfg.WebhooksDir)
	if e != nil {
		return nil, webhooks.ErrKey
	}
	if webhookWithin(keyPath, dataPath) || webhookWithin(keyPath, statePath) {
		return nil, webhooks.ErrKey
	}
	return webhooks.LoadKey(cfg.WebhooksKeyFile)
}

// InitializeWebhooks is an explicit offline provisioning action, never startup
// recovery. It refuses existing state and never generates/replaces key material.
func InitializeWebhooks(cfg *config.Config) error {
	if !cfg.WebhooksEnabled {
		return config.ErrWebhookSettings
	}
	if e := cfg.Validate(); e != nil {
		return e
	}
	key, e := loadWebhookKey(cfg)
	if e != nil {
		return e
	}
	defer clear(key)
	return webhooks.Initialize(webhooks.Config{Directory: cfg.WebhooksDir, EncryptionKey: key, AllowPrivateCIDRs: cfg.WebhooksAllowPrivateCIDRs, MaxPending: cfg.WebhooksMaxPending, Retention: cfg.WebhooksRetention})
}

func (s *Server) prepareWebhooks(ctx context.Context) error {
	if !s.cfg.WebhooksEnabled {
		return nil
	}
	if s.authn == nil || !s.authn.Enabled() {
		return errors.New("webhooks require authentication")
	}
	key, e := loadWebhookKey(s.cfg)
	if e != nil {
		return e
	}
	defer clear(key)
	svc, e := webhooks.OpenExisting(webhooks.Config{Directory: s.cfg.WebhooksDir, EncryptionKey: key, AllowPrivateCIDRs: s.cfg.WebhooksAllowPrivateCIDRs, MaxPending: s.cfg.WebhooksMaxPending, Retention: s.cfg.WebhooksRetention})
	if e != nil {
		return errors.New("webhook storage unavailable or insecure")
	}
	s.webhooks = svc
	if s.webhookDispatcher == nil {
		s.webhookDispatcher = &webhookDispatcher{s: s}
	}
	s.web.EnableWebhooks(svc, s.webhookDispatcher)
	go func() {
		if e := svc.Run(ctx); e != nil {
			log.Print("webhook delivery worker stopped; inspect webhook storage before retrying delivery")
		}
	}()
	if s.webhookDispatcher != nil {
		go func() {
			if e := svc.RunTriggers(ctx, s.webhookDispatcher); e != nil {
				log.Print("webhook trigger worker stopped; inspect request records and targets; do not retry uncertain operations")
			}
		}()
	}
	return nil
}
