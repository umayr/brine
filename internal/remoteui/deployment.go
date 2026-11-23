package remoteui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

const deployPath = "/remote/deployment"

var captureLimits = projectsnapshot.Limits{MaxFiles: 1000, MaxEntries: 2000, MaxDepth: 32, MaxFileBytes: 4 << 20, MaxTotalBytes: 16 << 20}

type Deployment struct {
	auth     *auth.Authenticator
	registry *targetregistry.Registry
	stacks   store.StackStore
	reviews  *projectreview.Store
	slot     chan struct{}
	mu       sync.Mutex
	pending  map[string]deploymentReview
	git      *git.Runner
	gitStore store.RemoteGitStore
	gitMu    sync.Mutex
}

type deploymentReview struct {
	Session, Host, Stack, StageID, Manifest, Selection, TargetDigest string
	Mode, PriorRequestID                                             string
	Expiry                                                           time.Time
}

const deploymentStatusUnavailable = "Status unavailable. Brine could not verify this request. This does not mean the deployment had no effect. Check the original target and its journal, and inspect the target before taking further action; do not retry based on this result."

func (p *deploymentPage) showGenericStatus(status agentclient.DeploymentStatus) {
	if status.State == "not-admitted" {
		// The generic lookup is not bound to the original journal or stack.
		p.Error = "Unverified status reply. This lookup cannot establish that the request was never admitted or had no effect. Check the original target and its journal, and inspect the target before taking further action; do not retry based on this result."
		return
	}
	p.Status = &status
}

type deploymentPage struct {
	CSRF, Host, Stack, ID, Error string
	Review                       *projectreview.Preview
	Plan                         *dill.PreflightResult
	Status                       *agentclient.DeploymentStatus
	Mode                         string
	GitSettings                  bool
	Branch, Source, Target       string
	GitPolicy                    *store.RemoteGitPolicy
	JournalCapacityLow           bool
}

func NewDeployment(a *auth.Authenticator, registry *targetregistry.Registry, stacks store.StackStore) (*Deployment, error) {
	if a == nil || !a.Enabled() || registry == nil || stacks == nil {
		return nil, errors.New("deployment UI unavailable")
	}
	reviews, err := projectreview.New(16, 64<<20, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &Deployment{auth: a, registry: registry, stacks: stacks, reviews: reviews, slot: make(chan struct{}, 4), pending: make(map[string]deploymentReview)}, nil
}

func (h *Deployment) Close() { h.reviews.Close() }

func (h *Deployment) Protected() http.Handler {
	inner := h.auth.Middleware(http.HandlerFunc(h.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		inner.ServeHTTP(w, r)
	})
}

func (h *Deployment) serve(w http.ResponseWriter, r *http.Request) {
	if !auth.CookieAuthenticated(r) {
		http.Error(w, "Interactive admin session required", 403)
		return
	}
	lease := r.Context()
	if r.Method == http.MethodPost {
		ctx, release, ok := h.auth.SubmissionLease(r)
		if !ok {
			http.Error(w, "Interactive admin session required", 403)
			return
		}
		defer release()
		lease = ctx
	}
	csrf := h.auth.CSRFFor(r)
	session := fmt.Sprintf("%x", sha256.Sum256([]byte("brine-remote-deployment\x00"+csrf)))
	if r.Method == http.MethodPost && r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	form := r.PostForm
	p := deploymentPage{CSRF: csrf, Host: r.URL.Query().Get("host"), ID: r.URL.Query().Get("id")}
	if r.Method == http.MethodPost {
		p.Host, p.Stack, p.ID = form.Get("host"), form.Get("stack"), form.Get("id")
	}
	if r.URL.Path == deployPath && r.Method == http.MethodGet && p.ID == "" {
		p.GitSettings = h.gitStore != nil
		h.render(w, p)
		return
	}
	if r.Method != http.MethodPost && (r.Method != http.MethodGet || r.URL.Path != deployPath || p.ID == "") {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != deployPath+"/create" && !(r.Method == http.MethodGet && p.ID != "") {
		select {
		case h.slot <- struct{}{}:
			defer func() { <-h.slot }()
		default:
			http.Error(w, "Deployment review busy", 503)
			return
		}
	}
	if r.URL.Path == deployPath+"/create" {
		review, ok := h.take(p.ID, session, form.Get("confirm"))
		if !ok {
			http.Error(w, "Missing, expired, or already used confirmation", 409)
			return
		}
		p.Host, p.Stack = review.Host, review.Stack
		if !h.gitMu.TryLock() {
			http.Error(w, "Remote automation busy; review again", 409)
			return
		}
		defer h.gitMu.Unlock()
		if stack, err := h.stacks.Get(p.Stack); err == nil && stack != nil && stack.RemoteGit != nil && (stack.RemoteGit.Enabled || stack.RemoteGit.Request != "") {
			http.Error(w, "Disable or reconcile remote Git automation before manual operations", 409)
			return
		}
		ctx, cancel := context.WithTimeout(lease, 6*time.Minute)
		defer cancel()
		record, releaseRecord, err := h.registry.AcquireRead(ctx, p.Host)
		if err != nil {
			http.Error(w, "Target unavailable; no creation sent", 409)
			return
		}
		defer releaseRecord.Close()
		if targetDigest(record) != review.TargetDigest {
			http.Error(w, "Target was re-paired; no creation sent", 409)
			return
		}
		c, epoch, err := openDeploymentSession(ctx, record)
		if err != nil {
			http.Error(w, "Target unavailable; no creation sent", 409)
			return
		}
		defer c.Close()
		var status agentclient.DeploymentStatus
		if review.Mode == "remove" {
			status, err = c.RemoveDeployment(ctx, epoch, review.Stack, review.Selection, p.ID, review.PriorRequestID)
		} else {
			status, err = c.ApplyDeployment(ctx, epoch, review.StageID, review.Manifest, review.Stack, review.Selection, p.ID, review.Mode)
		}
		if err == nil {
			p.Status = &status
		} else {
			p.Error = "Operation not confirmed. Look up this exact request before taking further action. No retry was sent."
			if err == agentclient.ErrJournalCapacity {
				p.JournalCapacityLow = true
			}
		}
		h.render(w, p)
		return
	}
	ctx, cancel := context.WithTimeout(lease, 75*time.Second)
	defer cancel()
	record, releaseRecord, err := h.registry.AcquireRead(ctx, p.Host)
	if err != nil || record.HTTPSAddress == "" {
		if releaseRecord != nil {
			_ = releaseRecord.Close()
		}
		if r.Method == http.MethodGet {
			p.Error = deploymentStatusUnavailable
			h.renderStatus(w, p, http.StatusConflict)
		} else {
			http.Error(w, "Paired HTTPS target unavailable", http.StatusConflict)
		}
		return
	}
	defer releaseRecord.Close()
	if r.Method == http.MethodGet {
		c, epoch, err := openDeploymentSession(ctx, record)
		if err == nil {
			defer c.Close()
			var status agentclient.DeploymentStatus
			status, err = c.DeploymentStatus(ctx, epoch, p.ID)
			if err == nil {
				p.showGenericStatus(status)
			}
		}
		if err != nil {
			p.Error = deploymentStatusUnavailable
		}
		h.render(w, p)
		return
	}
	current := projectreview.Current{Stack: p.Stack, Target: projectreview.Target{Kind: projectreview.HTTPS, Identity: targetDigest(record), Socket: record.HTTPSAddress, Endpoint: p.Host}}
	switch r.URL.Path {
	case deployPath + "/git", deployPath + "/git-config":
		h.gitSettings(ctx, w, r, &p, record)
		return
	case deployPath + "/review":
		stack, err := h.stacks.Get(p.Stack)
		if err != nil || stack == nil || (stack.Git != nil && stack.Git.Folder != "") || stack.PklPath == "" {
			http.Error(w, "Project unavailable; Git subfolders are not supported", 422)
			return
		}
		entry := stack.Entrypoint
		if entry == "" {
			entry = "compose.pkl"
		}
		snap, err := projectsnapshot.Build(filepath.Dir(stack.PklPath), entry, captureLimits)
		if err == nil && stack.Git == nil {
			// Locally owned projects share their directory with Brine metadata.
			snap.Files = slices.DeleteFunc(snap.Files, func(f projectsnapshot.File) bool { return f.Path == "meta.json" || strings.HasPrefix(f.Path, ".meta-") })
		}
		if err != nil || projectsnapshot.ValidateProvisionalReceiver(ctx, snap) != nil {
			http.Error(w, "Project capture unavailable or incompatible", 422)
			return
		}
		preview, err := h.reviews.Create(ctx, session, projectreview.Intent{Current: current, Snapshot: snap})
		if err != nil {
			http.Error(w, "Project review unavailable", 409)
			return
		}
		p.Review = &preview
	case deployPath + "/stage":
		if form.Get("confirm") != "transfer" {
			http.Error(w, "Transfer confirmation required", 400)
			return
		}
		token, err := h.reviews.Confirm(ctx, session, p.ID, current)
		if err != nil {
			http.Error(w, "Review unavailable; start a new review", 409)
			return
		}
		intent, err := h.reviews.Consume(ctx, session, p.ID, token, current)
		if err != nil {
			http.Error(w, "Review unavailable; no transfer sent", 409)
			return
		}
		c, epoch, err := openDeploymentSession(ctx, record)
		if err != nil {
			http.Error(w, "Target unavailable; no transfer sent", 409)
			return
		}
		defer c.Close()
		stage, manifest, plan, err := stageAndPreview(ctx, c, epoch, p.Stack, intent.Snapshot)
		if err != nil {
			p.Error = "Staging or preview could not be confirmed. Inspect the receiver before preparing another transfer. No retry was sent."
			p.ID = ""
			h.render(w, p)
			return
		}
		h.renderPlan(&p, session, current.Target.Identity, stage, manifest, plan)
	case deployPath + "/remove-preview":
		c, epoch, err := openDeploymentSession(ctx, record)
		if err != nil {
			http.Error(w, "Target unavailable", 409)
			return
		}
		defer c.Close()
		plan, err := c.PreviewRemoval(ctx, epoch, p.Stack)
		if err != nil {
			p.Error = "Managed removal preview unavailable. No operation was sent."
			break
		}
		h.renderPlan(&p, session, current.Target.Identity, "", "", plan)
	default:
		http.NotFound(w, r)
		return
	}
	h.render(w, p)
}

func (h *Deployment) renderPlan(p *deploymentPage, session, target, stage, manifest string, plan *agentclient.DeploymentPreview) {
	p.JournalCapacityLow = plan.JournalCapacityLow
	p.Plan, p.ID = plan.PreflightResult, ""
	if plan.Mode == "" || plan.SelectionDigest == nil || len(plan.Resources) == 0 {
		return
	}
	if plan.Mode == "create" {
		for _, r := range plan.Resources {
			if r.Presence != "absent" {
				return
			}
		}
	}
	p.ID = h.add(deploymentReview{Session: session, Host: p.Host, Stack: p.Stack, StageID: stage, Manifest: manifest, Selection: *plan.SelectionDigest, Mode: plan.Mode, PriorRequestID: plan.PriorRequestID, TargetDigest: target, Expiry: time.Now().Add(5 * time.Minute)})
	if p.ID != "" {
		p.Mode = plan.Mode
	}
}

func targetDigest(record targetregistry.Record) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(record.HTTPSAddress+"\x00"+string(record.HTTPSPairing))))
}

func openDeploymentSession(ctx context.Context, r targetregistry.Record) (*agentclient.Client, string, error) {
	if r.HTTPSAddress == "" {
		return nil, "", agentclient.ErrProtocol
	}
	p, err := agentclient.ParsePairing([]byte(r.HTTPSPairing))
	if err != nil {
		return nil, "", err
	}
	return p.Open(ctx, ctx, r.HTTPSAddress)
}

func stageAndPreview(ctx context.Context, c *agentclient.Client, epoch, stack string, snap projectsnapshot.Snapshot) (string, string, *agentclient.DeploymentPreview, error) {
	expected, err := dillserve.SnapshotManifestDigest(ctx, snap)
	if err != nil {
		return "", "", nil, err
	}
	type file struct {
		Path   string `json:"path"`
		Bytes  []byte `json:"bytes"`
		Length int    `json:"length"`
		SHA256 string `json:"sha256"`
	}
	files := make([]file, 0, len(snap.Files))
	for _, f := range snap.Files {
		b := f.Content
		if b == nil {
			b = []byte{}
		}
		files = append(files, file{f.Path, b, len(b), hex.EncodeToString(f.SHA256[:])})
	}
	body, err := json.Marshal(struct {
		Schema     string `json:"schema"`
		Entrypoint string `json:"entrypoint"`
		Files      []file `json:"files"`
	}{"dill.snapshot.v1", snap.Entrypoint, files})
	if err != nil {
		return "", "", nil, err
	}
	id, err := c.Stage(ctx, epoch, body)
	if err != nil {
		return "", "", nil, err
	}
	m, err := c.Snapshot(ctx, epoch, id)
	if err != nil || m.Digest() != expected || !manifestMatches(m, snap) {
		return id, "", nil, agentclient.ErrProtocol
	}
	digest := fmt.Sprintf("%x", expected)
	plan, err := c.PreviewDeployment(ctx, epoch, id, digest, stack)
	return id, digest, plan, err
}

func manifestMatches(m dillserve.Manifest, s projectsnapshot.Snapshot) bool {
	files := m.Files()
	if m.Entrypoint() != s.Entrypoint || len(files) != len(s.Files) {
		return false
	}
	for i, f := range files {
		if f.Path != s.Files[i].Path || f.Size != int64(len(s.Files[i].Content)) || f.SHA256 != s.Files[i].SHA256 {
			return false
		}
	}
	return true
}

func (h *Deployment) add(v deploymentReview) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, r := range h.pending {
		if time.Now().After(r.Expiry) {
			delete(h.pending, k)
		}
	}
	if len(h.pending) >= 64 {
		return ""
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(raw[:])
	if _, exists := h.pending[id]; exists {
		return ""
	}
	h.pending[id] = v
	return id
}
func (h *Deployment) take(id, session, confirm string) (deploymentReview, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.pending[id]
	if !ok || r.Session != session || confirm != r.Mode || !time.Now().Before(r.Expiry) {
		return deploymentReview{}, false
	}
	delete(h.pending, id)
	return r, true
}

func (h *Deployment) render(w http.ResponseWriter, p deploymentPage) {
	h.renderStatus(w, p, http.StatusOK)
}

func (h *Deployment) renderStatus(w http.ResponseWriter, p deploymentPage, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = deploymentView.Execute(w, p)
}

func IsDeploymentPath(path string) bool {
	return path == deployPath || strings.HasPrefix(path, deployPath+"/")
}

var deploymentView = template.Must(template.New("deployment").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Remote deployment</title><main><h1>Remote deployment</h1><p><a href="/remote">Remote hosts</a></p>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{if .JournalCapacityLow}}<p role="alert">Dill's journal is nearing its size limit. Preserve the journal; do not delete it to make room.</p>{{end}}<p>Target: <bdi>{{.Host}}</bdi></p>
{{if and .ID (not .Review)}}<p><label for="deployment-request-id">Request ID</label><br><input type="text" id="deployment-request-id" value="{{.ID}}" readonly size="34" style="max-width:100%;box-sizing:border-box" aria-describedby="deployment-request-help"></p>
<p id="deployment-request-help">Select and copy the full request ID, or save the status link to return after a disconnect.</p>
<p><a href="/remote/deployment?host={{.Host|urlquery}}&amp;id={{.ID|urlquery}}">{{if .Plan}}Open status link{{else}}Refresh status{{end}}</a> · Checks this request without resubmitting it.</p>{{end}}
{{if .Status}}<p>Stack {{.Status.Stack}} · State {{.Status.State}} · Effects {{.Status.Effects}}{{if .Status.OperationID}} · Dill operation <code>{{.Status.OperationID}}</code>{{end}}</p>{{if eq .Status.State "unknown"}}<p role="alert">Outcome unknown. Dill recorded this request but could not determine its outcome. Inspect the target before taking further action; do not retry based on this result.</p>{{else if and (eq .Status.State "failed") (eq .Status.Effects "partial")}}<p role="alert">The operation failed after partially changing the target. Inspect the target and the recorded Dill operation before taking further action; do not retry based on this result.</p>{{end}}{{end}}
{{if .Review}}<h2>Review captured files</h2><p>Only transfer trusted projects. Staging may contain credentials; the configured target can evaluate Pkl with the agent account’s privileges. This is not a sandbox. Effective Brine variables are not transferred. No files have been sent yet.</p><p>Entrypoint: <code>{{.Review.Entrypoint}}</code></p><ul>{{range .Review.Files}}<li>{{.Path}} · {{.Size}} bytes · <code>{{printf "%x" .SHA256}}</code></li>{{end}}</ul>
<form method="post" action="/remote/deployment/stage"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="id" value="{{.Review.ID}}"><label><input type="checkbox" name="confirm" value="transfer" required> Send these exact files and request a preview</label><button>Stage and preview</button></form>
{{else if .Plan}}<h2>Deployment preview for {{.Stack}}</h2><ul>{{range .Plan.Resources}}<li>{{.Kind}} {{.Name}} · {{.Presence}} · {{.Action}}{{if .Retention}} · {{.Retention}}{{end}}</li>{{end}}</ul>
{{if .Mode}}<p>This preview did not permit writes. Save the status link before confirming if you need to recover after a disconnect.</p><form method="post" action="/remote/deployment/create"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><label><input type="checkbox" name="confirm" value="{{.Mode}}" required> Confirm {{.Mode}} for this stack on this target</label><button>{{if eq .Mode "create"}}Create stack{{else if eq .Mode "update"}}Update stack{{else}}Remove containers; keep data{{end}}</button></form>{{else}}<p>Mutation unavailable. Existing resources are not adopted.</p>{{end}}
{{else if .GitPolicy}}<h2>Remote Git automation for {{.Stack}}</h2><p>Selected target: {{.Host}} · Branch: <code>{{.Branch}}</code> · State: {{.GitPolicy.Phase}} · Commit: <code>{{.GitPolicy.Commit}}</code></p>
{{if .GitPolicy.AttemptHistoryLow}}<p role="alert">{{if .GitPolicy.CanRecordAttempt}}The Git attempt history is nearing its size limit.{{else}}The Git attempt history is full. No new commits will be sent.{{end}} Preserve the recorded attempts.</p>{{end}}
{{with .GitPolicy.Reason}}<p>{{if eq . "awaiting-status"}}No terminal status has been recorded yet.{{else if eq . "running"}}Dill admitted the request and it is running.{{else if eq . "unknown"}}Dill admitted the request but cannot prove its outcome. Inspect the target; do not retry.{{else if eq . "status-unavailable"}}The exact request or its original journal could not be verified.{{else if eq . "staging-or-preview"}}Staging or preview did not complete. Inspect the receiver before taking further action.{{else if eq . "journal-capacity"}}Dill reports that its journal is near capacity. No apply was sent. Preserve the journal and inspect agent state.{{else if eq . "no-journal"}}The agent does not advertise a recoverable journal. No apply was sent.{{else if eq . "not-admitted"}}Dill fenced the request without admitting it. Automation is disabled and the same commit will not be resubmitted.{{else if eq . "failed"}}Dill reported a failure. Inspect the target before re-enabling automation.{{end}}</p>{{end}}
{{if .GitPolicy.Request}}<p><a href="/remote/deployment?host={{.GitPolicy.Host|urlquery}}&amp;id={{.GitPolicy.Request|urlquery}}">Look up pending request <code>{{.GitPolicy.Request}}</code></a>. A missing result is not evidence of no effects.</p>
{{if and .GitPolicy.Journal (eq .Target .GitPolicy.Target)}}<form method="post" action="/remote/deployment/git-config"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="target" value="{{.Target}}"><input type="hidden" name="request" value="{{.GitPolicy.Request}}"><label><input type="checkbox" name="confirm" value="fence-unadmitted" required> Forbid this exact request from ever starting if Dill has not admitted it. This cannot cancel an admitted operation.</label><button name="mode" value="recover">Check and fence request</button></form>{{else}}<p>Fencing is unavailable without the original target and a journal binding.</p>{{end}}{{end}}
<form method="post" action="/remote/deployment/git-config"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="target" value="{{.Target}}">{{if .GitPolicy.Enabled}}<button name="mode" value="disable">Disable remote auto-deploy</button>{{else if not .GitPolicy.Request}}<label><input type="checkbox" name="confirm" value="trusted" required> Authorize this Git branch to send exact commits and create or update this stack without individual review. The target evaluates trusted Pkl with agent privileges.</label><button name="mode" value="enable">Enable remote auto-deploy</button>{{end}}</form>
{{else if not .ID}}<form method="post" action="/remote/deployment/review"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Brine project name <input name="stack" required value="{{.Stack}}"></label><button>Review project files</button></form>
<form method="post" action="/remote/deployment/remove-preview"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Managed stack name <input name="stack" required value="{{.Stack}}"></label><button>Preview removal</button></form>
{{if .GitSettings}}<form method="post" action="/remote/deployment/git"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Git-backed Brine project <input name="stack" required></label><button>Remote auto-deploy settings</button></form>{{end}}{{end}}
</main></html>`))
