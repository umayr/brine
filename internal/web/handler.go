package web

import (
	"sync"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
)

// Handler holds shared dependencies for all web handlers.
type Handler struct {
	agent         agent.Agent
	store         store.StackStore
	recipes       store.RecipeStore
	git           *git.Runner
	auth          *auth.Authenticator
	dev           bool // re-parse templates on every request when true
	engine        string
	deployTimeout time.Duration

	mu sync.RWMutex
	ts *templateSet // cached template set (nil in dev mode)
}

// New creates a Handler.
func New(ag agent.Agent, st store.StackStore, rs store.RecipeStore, gr *git.Runner, engine string, dev bool, authn *auth.Authenticator, deployTimeout time.Duration) *Handler {
	return &Handler{
		agent:         ag,
		store:         st,
		recipes:       rs,
		git:           gr,
		auth:          authn,
		engine:        engine,
		dev:           dev,
		deployTimeout: deployTimeout,
	}
}

// templates returns the parsed template set, using the cache unless in dev mode.
func (h *Handler) templates() (*templateSet, error) {
	if h.dev {
		return parseTemplates()
	}
	h.mu.RLock()
	ts := h.ts
	h.mu.RUnlock()
	if ts != nil {
		return ts, nil
	}
	// Parse once and cache.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ts != nil {
		return h.ts, nil
	}
	ts, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	h.ts = ts
	return ts, nil
}
