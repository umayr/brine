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
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
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
}

type deploymentReview struct {
	Session, Host, Stack, StageID, Manifest, Selection, TargetDigest string
	Expiry                                                           time.Time
}

type deploymentPage struct {
	CSRF, Host, Stack, ID, Error string
	Review                       *projectreview.Preview
	Plan                         *dill.PreflightResult
	Status                       *agentclient.DeploymentStatus
	CanCreate                    bool
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
		status, err := c.CreateDeployment(ctx, epoch, review.StageID, review.Manifest, review.Stack, review.Selection, p.ID)
		if err == nil {
			p.Status = &status
		} else {
			p.Error = "Creation not confirmed. Look up this exact request before taking further action. No retry was sent."
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
		http.Error(w, "Paired HTTPS target unavailable", 409)
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
				p.Status = &status
			}
		}
		if err != nil {
			p.Error = "No outcome verified for this request. Unavailable is not proof that no change occurred."
		}
		h.render(w, p)
		return
	}
	current := projectreview.Current{Stack: p.Stack, Target: projectreview.Target{Kind: projectreview.HTTPS, Identity: targetDigest(record), Socket: record.HTTPSAddress, Endpoint: p.Host}}
	switch r.URL.Path {
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
		p.Plan = plan.PreflightResult
		p.ID = ""
		if plan.CreateEnabled && plan.SelectionDigest != nil && len(plan.Resources) > 0 {
			p.CanCreate = true
			for _, resource := range plan.Resources {
				if resource.Presence != "absent" {
					p.CanCreate = false
				}
			}
		}
		if p.CanCreate {
			p.ID = h.add(deploymentReview{Session: session, Host: p.Host, Stack: p.Stack, StageID: stage, Manifest: manifest, Selection: *plan.SelectionDigest, TargetDigest: current.Target.Identity, Expiry: time.Now().Add(5 * time.Minute)})
			p.CanCreate = p.ID != ""
		}
	default:
		http.NotFound(w, r)
		return
	}
	h.render(w, p)
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
	if err := projectsnapshot.ValidateProvisionalReceiver(ctx, snap); err != nil {
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
	if err != nil || !manifestMatches(m, snap) {
		return id, "", nil, agentclient.ErrProtocol
	}
	digest := fmt.Sprintf("%x", m.Digest())
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
	if !ok || r.Session != session || confirm != "create" || !time.Now().Before(r.Expiry) {
		return deploymentReview{}, false
	}
	delete(h.pending, id)
	return r, true
}

func (h *Deployment) render(w http.ResponseWriter, p deploymentPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = deploymentView.Execute(w, p)
}

func IsDeploymentPath(path string) bool {
	return path == deployPath || strings.HasPrefix(path, deployPath+"/")
}

var deploymentView = template.Must(template.New("deployment").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Remote deployment</title><main><h1>Remote deployment</h1><p><a href="/remote">Remote hosts</a></p>
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}<p>Target: <bdi>{{.Host}}</bdi></p>
{{if .Status}}<p>Stack {{.Status.Stack}} · State {{.Status.State}} · Effects {{.Status.Effects}}{{if .Status.OperationID}} · Dill operation <code>{{.Status.OperationID}}</code>{{end}}</p>{{end}}
{{if .Review}}<h2>Review captured files</h2><p>Only transfer trusted projects. Staging may contain credentials; the configured target can evaluate Pkl with the agent account’s privileges. This is not a sandbox. Effective Brine variables are not transferred. No files have been sent yet.</p><p>Entrypoint: <code>{{.Review.Entrypoint}}</code></p><ul>{{range .Review.Files}}<li>{{.Path}} · {{.Size}} bytes · <code>{{printf "%x" .SHA256}}</code></li>{{end}}</ul>
<form method="post" action="/remote/deployment/stage"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="id" value="{{.Review.ID}}"><label><input type="checkbox" name="confirm" value="transfer" required> Send these exact files and request a preview</label><button>Stage and preview</button></form>
{{else if .Plan}}<h2>Deployment preview for {{.Stack}}</h2><ul>{{range .Plan.Resources}}<li>{{.Kind}} {{.Name}} · {{.Presence}} · {{.Action}}{{if .Retention}} · {{.Retention}}{{end}}</li>{{end}}</ul>
{{if .CanCreate}}<p>This preview did not permit writes. Save the status link before confirming if you need to recover after a disconnect.</p><form method="post" action="/remote/deployment/create"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><label><input type="checkbox" name="confirm" value="create" required> Create this new managed stack on this target</label><button>Create stack</button></form>{{else}}<p>Creation unavailable: either the host has not enabled it or the preview includes existing resources. Existing resources are not adopted.</p>{{end}}
{{else if not .ID}}<form method="post" action="/remote/deployment/review"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Brine project name <input name="stack" required value="{{.Stack}}"></label><button>Review project files</button></form>{{end}}
{{if .ID}}{{if not .Review}}<p><a href="/remote/deployment?host={{.Host|urlquery}}&amp;id={{.ID|urlquery}}">Look up creation request <code>{{.ID}}</code></a></p>{{end}}{{end}}</main></html>`))
