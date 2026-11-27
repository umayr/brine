package web

import (
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/store"
)

// Handler holds shared dependencies for all web handlers.
type Handler struct {
	agent            agent.Agent
	store            store.StackStore
	recipes          store.RecipeStore
	git              *git.Runner
	auth             *auth.Authenticator
	dev              bool // read templates and static files from the source tree
	devFS            fs.FS
	engine           string
	deployTimeout    time.Duration
	stagingConfig    *config.Config
	stagingStore     *projectreview.Store
	stagingAdmission chan struct{}
	stagingLifecycle *stagingLifecycle
	remoteEnabled    bool
	sourceHistory    *sourceHistoryAccess
	broker           *brokerapp.Service
	brokerController *brokerapp.Controller
	brokerTargets    brokerTargetOpener

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
		devFS:         os.DirFS("internal/web"),
		deployTimeout: deployTimeout,
	}
}

// EnableBroker is startup-only, before requests are served.
func (h *Handler) EnableBroker(service *brokerapp.Service) {
	h.broker = service
	if service.Enabled() {
		h.brokerController, _ = brokerapp.NewController(service, h.brokerGuard)
	}
}

// EnableRemoteNavigation is called before serving after successful remote initialization.
func (h *Handler) EnableRemoteNavigation() { h.remoteEnabled = true }

// templates returns the parsed template set, using the cache unless in dev mode.
func (h *Handler) templates() (*templateSet, error) {
	if h.dev {
		return parseTemplatesFS(h.devFS)
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
