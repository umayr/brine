package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/projectstaging"
)

// stagingLifecycle linearizes close against the decision to submit. Close
// cancels in-flight I/O but does not wait for (or recall) an already-won POST.
type stagingLifecycle struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

func newStagingLifecycle() *stagingLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &stagingLifecycle{ctx: ctx, cancel: cancel}
}

func (l *stagingLifecycle) request(r *http.Request) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(l.ctx, cancel)
	if l.ctx.Err() != nil {
		cancel()
	}
	return r.WithContext(ctx), func() { stop(); cancel() }
}

// EnableLocalStaging must be called before the handler is served. Configuration
// is startup-owned and must remain immutable while the server is running.
func (h *Handler) EnableLocalStaging(cfg *config.Config) {
	if cfg == nil || cfg.Validate() != nil || cfg.StagingSocket == "" || cfg.StagingIdentity == "" {
		return
	}
	s, err := projectreview.New(8, 64<<20, 5*time.Minute)
	if err == nil {
		h.stagingConfig, h.stagingStore = cfg, s
		h.stagingAdmission = make(chan struct{}, 8)
		h.stagingLifecycle = newStagingLifecycle()
	}
}
func (h *Handler) CloseStaging() {
	if l := h.stagingLifecycle; l != nil {
		l.mu.Lock()
		if !l.closed {
			l.closed = true
			l.cancel()
		}
		l.mu.Unlock()
	}
	if h.stagingStore != nil {
		h.stagingStore.Close()
	}
}

func (h *Handler) stagingSession(r *http.Request) string {
	if h.auth == nil || !h.auth.Enabled() {
		return ""
	}
	csrf := h.auth.CSRFFor(r)
	if csrf == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("brine-staging-session-v1\x00" + csrf))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) stagingSender(name string) (*projectstaging.Sender, error) {
	if h.stagingStore == nil || h.stagingConfig == nil || h.agent == nil || h.agent.Dill() == nil || h.agent.Dill().Variables == nil {
		return nil, projectstaging.ErrInvalid
	}
	cfg := h.stagingConfig
	target := dillserve.Target{Socket: cfg.StagingSocket, Stack: name, Engine: cfg.Engine}
	return projectstaging.New(h.stagingStore, target, cfg.StagingIdentity, func(ctx context.Context) (projectreview.Current, error) {
		var c projectreview.Current
		if cfg.Validate() != nil || cfg.StagingSocket == "" || cfg.StagingIdentity == "" || h.agent.Dill().Variables == nil {
			return c, projectstaging.ErrInvalid
		}
		stack, err := h.store.Get(name)
		if err != nil || stack == nil || stack.CreatedAt.IsZero() || (stack.Git != nil && stack.Git.Folder != "") {
			return c, projectstaging.ErrInvalid
		}
		b, err := json.Marshal(stack)
		if err != nil {
			return c, projectstaging.ErrInvalid
		}
		hash := sha256.Sum256(b)
		vars, err := h.agent.Dill().Variables.Effective(name)
		if err != nil {
			return c, projectstaging.ErrInvalid
		}
		if h.variableModeDisabled() {
			vars = map[string]string{}
		}
		sourceType := "local stack"
		if stack.Git != nil {
			sourceType = "Git repository"
		}
		c = projectreview.Current{Stack: name, Target: projectreview.Target{Kind: projectreview.Local, Socket: cfg.StagingSocket, Identity: cfg.StagingIdentity}, Variables: vars, Options: map[string]string{"engine": cfg.Engine, "stack-metadata": hex.EncodeToString(hash[:]), "source-root": filepath.Dir(stack.PklPath), "source-type": sourceType, "profile": h.agent.Dill().Profile, "variable-mode-disabled": fmt.Sprint(h.variableModeDisabled())}}
		return c, ctx.Err()
	})
}

var stagingReviewTemplate = template.Must(template.New("staging").Parse(`
<div role="status" class="space-y-2">
 <p><strong>Review the complete captured file list and hashes before transfer.</strong> File contents are not displayed and cannot be inspected here. No bytes have been sent.</p>
 <p>Target socket: <code>{{.Current.Target.Socket}}</code>; asserted identity: <code>{{.Current.Target.Identity}}</code>; stack: <code>{{.Current.Stack}}</code>; engine: <code>{{index .Current.Options "engine"}}</code>. Socket access controls the receiver; identity is an assertion, not attestation. Remote/SSH staging is unsupported.</p>
 <p>Source type: <code>{{index .Current.Options "source-type"}}</code>; captured source root: <code class="whitespace-pre-wrap">{{index .Current.Options "source-root"}}</code>. Entrypoint: <code>{{.Entrypoint}}</code>. Expires: {{.Expires}}.</p>
 <p>Files may contain secrets, including .env and keys. This list does not inspect contents; hashes may themselves be sensitive. Ignored, untracked and hidden files are included; .gitignore is not honored. Hard links and mounted subdirectories are included and not detected or filtered; .git entries are omitted. Local projects include Brine's meta.json; Git projects use the repository root, and Git subfolders are unavailable. Symlinks and special files are rejected. Empty directories and modes are not preserved. Root ancestors and same-user processes must be trusted. Concurrent changes mean capture is not an atomic filesystem snapshot; a later capture may differ.</p>
 <p>Transfer only: no evaluation or deployment follows. Provisional receiver compatibility includes the encoded upload size limit and is not an execution guarantee. Limits: 1,000 files, 2,000 entries, 32 levels, 4 MiB per file and 16 MiB total; exceeding a limit fails capture. Project root ancestors and other same-user processes are not sandboxed.</p>
 <ul aria-label="Complete captured files">{{range .Files}}<li><code class="whitespace-pre-wrap">{{.Path}}</code> — {{.Size}} bytes — SHA-256 <code>{{printf "%x" .SHA256}}</code></li>{{end}}</ul>
 <p>Effective non-secret variables:</p><ul>{{range $k,$v := .Current.Variables}}<li><code>{{$k}}</code> = <code class="whitespace-pre-wrap">{{$v}}</code></li>{{else}}<li>None</li>{{end}}</ul>
 <form hx-post="/web/stacks/{{.Current.Stack}}/staging/confirm" hx-target="#staging-result" hx-swap="innerHTML" hx-on::response-error="document.getElementById('staging-result').textContent = event.detail.xhr.responseText" hx-on::send-error="document.getElementById('staging-result').textContent = 'Transfer outcome unknown. Do not retry; inspect the receiver before preparing another review.'" hx-on::timeout="document.getElementById('staging-result').textContent = 'Transfer outcome unknown. Do not retry; inspect the receiver before preparing another review.'"><input type="hidden" name="review" value="{{.ID}}"><button type="submit" class="ui-button ui-button-outline ui-button-sm">Confirm and transfer these captured bytes</button></form>
</div>`))

// Admission bounds goroutines waiting on the legacy non-cancellable stack
// coordinator. Cancellation is rechecked immediately after that lock is held.
func (h *Handler) admitStaging(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if h.stagingAdmission == nil {
		http.Error(w, "Local staging unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	if r.Context().Err() != nil {
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return nil, false
	}
	select {
	case h.stagingAdmission <- struct{}{}:
		return func() { <-h.stagingAdmission }, true
	default:
		http.Error(w, "Local staging busy", http.StatusServiceUnavailable)
		return nil, false
	}
}

func (h *Handler) stagingPrepare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session := h.stagingSession(r)
	if session == "" {
		http.Error(w, "Authenticated cookie session required", http.StatusUnauthorized)
		return
	}
	releaseAdmission, ok := h.admitStaging(w, r)
	if !ok {
		return
	}
	defer releaseAdmission()
	r, releaseContext := h.stagingLifecycle.request(r)
	defer releaseContext()
	name := chi.URLParam(r, "name")
	unlock, admitted := h.admitStack(w, r, name)
	if !admitted {
		return
	}
	defer unlock()
	if r.Context().Err() != nil {
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return
	}
	sender, err := h.stagingSender(name)
	if err != nil {
		http.Error(w, "Local staging unavailable", http.StatusServiceUnavailable)
		return
	}
	stack, err := h.store.Get(name)
	if err != nil || stack == nil || (stack.Git != nil && stack.Git.Folder != "") {
		http.Error(w, "Snapshot unavailable", http.StatusUnprocessableEntity)
		return
	}
	metadata, err := json.Marshal(stack)
	if err != nil {
		http.Error(w, "Snapshot unavailable", http.StatusUnprocessableEntity)
		return
	}
	capturedMetadata := sha256.Sum256(metadata)
	entry := stack.Entrypoint
	if entry == "" {
		entry = "compose.pkl"
	}
	snap, err := projectsnapshot.Build(filepath.Dir(stack.PklPath), entry, previewLimits)
	if err != nil {
		http.Error(w, "Snapshot unavailable; check files and limits", http.StatusUnprocessableEntity)
		return
	}
	// The sender requires a writer-excluding configuration lease for the entire call.
	releaseVariables, err := h.agent.Dill().Variables.ReadLease(r.Context())
	if err != nil {
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return
	}
	defer releaseVariables()
	p, err := sender.Create(r.Context(), session, snap)
	if err == nil && (p.Current.Options["source-root"] != filepath.Dir(stack.PklPath) || p.Current.Options["stack-metadata"] != hex.EncodeToString(capturedMetadata[:])) {
		err = projectstaging.ErrInvalid
	}
	if err != nil {
		http.Error(w, "Snapshot review unavailable or incompatible", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = stagingReviewTemplate.Execute(w, p)
}
func (h *Handler) stagingConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session := h.stagingSession(r)
	if session == "" {
		http.Error(w, "Authenticated cookie session required", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	releaseAdmission, ok := h.admitStaging(w, r)
	if !ok {
		return
	}
	defer releaseAdmission()
	r, releaseContext := h.stagingLifecycle.request(r)
	defer releaseContext()
	name := chi.URLParam(r, "name")
	unlock, admitted := h.admitStack(w, r, name)
	if !admitted {
		return
	}
	defer unlock()
	if r.Context().Err() != nil {
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return
	}
	sender, err := h.stagingSender(name)
	if err != nil {
		http.Error(w, "Local staging unavailable", http.StatusServiceUnavailable)
		return
	}
	// Lock order: stack coordinator, variables read lease, session submission
	// lease (acquired by sender just before POST). Variable writes never take
	// the stack coordinator; logout never takes either preceding lock.
	releaseVariables, err := h.agent.Dill().Variables.ReadLease(r.Context())
	if err != nil {
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return
	}
	defer releaseVariables()
	id := r.PostForm.Get("review")
	token, err := sender.Confirm(r.Context(), session, id)
	if err != nil {
		http.Error(w, "Review unavailable or configuration changed; prepare again", http.StatusConflict)
		return
	}
	stageID, err := sender.StageWithSubmissionLease(r.Context(), session, id, token, func(context.Context) (context.Context, func(), error) {
		ctx, release, ok := h.auth.SubmissionLease(r)
		if !ok {
			return nil, nil, projectstaging.ErrInvalid
		}
		// This lock is the shutdown/submission linearization point. A close
		// that wins denies submission, even after consent was consumed. If
		// submission wins, close cancels I/O but cannot recall an upload.
		l := h.stagingLifecycle
		l.mu.Lock()
		if l.closed || ctx.Err() != nil {
			l.mu.Unlock()
			release()
			return nil, nil, projectstaging.ErrInvalid
		}
		l.mu.Unlock()
		return ctx, release, nil
	})
	if errors.Is(err, dillserve.ErrStageUnknown) {
		http.Error(w, "Transfer outcome unknown. Do not retry; inspect the receiver before preparing another review.", http.StatusBadGateway)
		return
	}
	if err != nil {
		http.Error(w, "Transfer was not confirmed successful. Do not retry automatically; inspect the receiver before preparing another review.", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Snapshot staged (transfer only). Stage ID: " + stageID))
}
