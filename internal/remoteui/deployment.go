package remoteui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	auth          *auth.Authenticator
	credentials   DeploymentCredentials
	registry      *targetregistry.Registry
	stacks        store.StackStore
	reviews       *projectreview.Store
	slot          chan struct{}
	mu            sync.Mutex
	pending       map[string]deploymentReview
	attendedBytes int
	closed        bool
	history       *attendedHistory // nil for every production constructor
	git           *git.Runner
	gitStore      store.RemoteGitStore
	gitAdmission  deploymentAdmission
}

type deploymentReview struct {
	Session, Host, Stack, StageID, Manifest, Selection, TargetDigest, Journal string
	CredentialRevision                                                        string
	Mode, PriorRequestID                                                      string
	Expiry                                                                    time.Time
	Attended                                                                  *attendedReview
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
	ManualIntents                []store.ManualDeploymentIntent
	ManualRecover                bool
	CSRF, Host, Stack, ID, Error string
	Review                       *projectreview.Preview
	Plan                         *dill.PreflightResult
	Status                       *agentclient.DeploymentStatus
	Mode                         string
	GitSettings                  bool
	Branch, Source, Target       string
	GitPolicy                    *store.RemoteGitPolicy
	JournalCapacityLow           bool
	Attended                     bool
	Evidence                     string
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

func (h *Deployment) Close() {
	h.reviews.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id := range h.pending {
		h.dropReview(id)
	}
}

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
	session := deploymentSession(csrf)
	if r.Method == http.MethodPost && r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	form := r.PostForm
	p := deploymentPage{CSRF: csrf, Host: r.URL.Query().Get("host"), ID: r.URL.Query().Get("id")}
	if r.Method == http.MethodPost {
		p.Host, p.Stack, p.ID = form.Get("host"), form.Get("stack"), form.Get("id")
	}
	if r.URL.Path == deployPath+"/recover" && r.Method == http.MethodPost {
		h.recoverManual(lease, w, r, &p)
		return
	}
	if r.URL.Path == deployPath && r.Method == http.MethodGet && p.ID == "" {
		if intents, ok := h.stacks.(store.ManualDeploymentStore); ok {
			rows, err := intents.ListManualIntents()
			if err != nil {
				p.Error = deploymentStatusUnavailable
			} else {
				for _, intent := range rows {
					if !intent.Resolved {
						p.ManualIntents = append(p.ManualIntents, intent)
					}
				}
			}
		}
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
		if h.history != nil && h.history.operator != nil && review.Mode == "remove" {
			http.Error(w, "Operator-configured authority is for new deployments only", http.StatusConflict)
			return
		}
		p.Host, p.Stack = review.Host, review.Stack
		releaseAdmission, admitted := h.gitAdmission.try(p.Stack)
		if !admitted {
			http.Error(w, "Remote automation busy; review again", 409)
			return
		}
		defer releaseAdmission()
		if !h.manualClear(p.Stack) {
			http.Error(w, "Unresolved manual recovery or unavailable recovery store; no operation sent", 409)
			return
		}
		stack, stackErr := h.stacks.Get(p.Stack)
		// A retained attended review cannot become a legacy confirmation
		// when the history backend is no longer available.
		historyRequired := review.Attended != nil || (h.history != nil && review.Mode != "remove")
		// Removal may target a deployment without a local registration, but
		// unreadable metadata is never evidence that automation is absent.
		absentRemoval := !historyRequired && review.Mode == "remove" && errors.Is(stackErr, store.ErrStackNotFound)
		if !absentRemoval && (stackErr != nil || stack == nil || stack.Name != p.Stack) {
			http.Error(w, "Cannot verify remote Git overlap; no operation sent", http.StatusConflict)
			return
		}
		if stackErr == nil && stack != nil && stack.RemoteGit != nil && (stack.RemoteGit.Enabled || stack.RemoteGit.Request != "") {
			http.Error(w, "Disable or reconcile remote Git automation before manual operations", 409)
			return
		}
		ctx, cancel := context.WithTimeout(lease, 6*time.Minute)
		defer cancel()
		record, enrollment, releaseRecord, err := h.acquireDeploymentTarget(ctx, p.Host, historyRequired)
		if err != nil {
			http.Error(w, "Target unavailable; no creation sent", 409)
			return
		}
		defer releaseRecord.Close()
		ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
		if targetDigest(record) != review.TargetDigest || (!historyRequired && h.deploymentCredentialRevision(ctx, record) != review.CredentialRevision) {
			http.Error(w, "Target was re-paired; no creation sent", 409)
			return
		}
		if historyRequired {
			h.submitAttended(ctx, w, p, review, record, enrollment)
			return
		}
		c, epoch, err := h.openDeploymentSession(ctx, record, review.CredentialRevision)
		if err != nil {
			http.Error(w, "Target unavailable; no creation sent", 409)
			return
		}
		defer c.Close()
		intents, ok := h.stacks.(store.ManualDeploymentStore)
		intent := store.ManualDeploymentIntent{Host: review.Host, Target: review.TargetDigest, Stack: review.Stack, Mode: review.Mode, Request: p.ID, PriorRequest: review.PriorRequestID, Journal: review.Journal, Selection: review.Selection, Stage: review.StageID, Manifest: review.Manifest}
		if !ok || intents.SaveManualIntent(intent) != nil {
			http.Error(w, "Durable recovery unavailable or unresolved operation exists; no operation sent", http.StatusConflict)
			return
		}
		var status agentclient.DeploymentStatus
		if review.Mode == "remove" {
			status, err = c.RemoveJournalDeployment(ctx, epoch, review.Stack, review.Selection, p.ID, review.PriorRequestID, review.Journal)
		} else {
			status, err = c.ApplyJournalDeployment(ctx, epoch, review.StageID, review.Manifest, review.Stack, review.Selection, p.ID, review.Mode, review.Journal)
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
	record, enrollment, releaseRecord, err := h.acquireDeploymentTarget(ctx, p.Host, r.URL.Path == deployPath+"/stage")
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
	ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
	if r.Method == http.MethodGet {
		if h.history != nil && h.history.operator != nil {
			p.Error = deploymentStatusUnavailable
			h.renderStatus(w, p, http.StatusConflict)
			return
		}
		var saved *store.ManualDeploymentIntent
		if intents, ok := h.stacks.(store.ManualDeploymentStore); ok {
			rows, loadErr := intents.ListManualIntents()
			if loadErr != nil {
				p.Error = deploymentStatusUnavailable
				h.renderStatus(w, p, http.StatusConflict)
				return
			}
			for _, intent := range rows {
				if intent.Request == p.ID {
					saved = &intent
					break
				}
			}
		}
		if saved != nil && (saved.Host != p.Host || saved.Target != targetDigest(record)) {
			p.Error = deploymentStatusUnavailable
			h.renderStatus(w, p, http.StatusConflict)
			return
		}
		c, epoch, err := h.openDeploymentSession(ctx, record)
		if err == nil {
			defer c.Close()
			var status agentclient.DeploymentStatus
			if saved != nil {
				p.Stack = saved.Stack
				p.ManualRecover = !saved.Resolved
				status, err = c.JournalDeploymentStatus(ctx, epoch, saved.Request, saved.Stack, saved.Journal)
				if err == nil {
					p.Status = &status
				}
			} else {
				status, err = c.DeploymentStatus(ctx, epoch, p.ID)
				if err == nil {
					p.showGenericStatus(status)
				}
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
		if intent.PriorDeployment != nil && (h.history == nil || h.history.restoreGate == nil || h.history.operator != nil) {
			http.Error(w, "Archived configuration is not authorized for review; no transfer sent", http.StatusConflict)
			return
		}
		credentialRevision := h.deploymentCredentialRevision(ctx, record)
		var stager deploymentStager
		var epoch string
		var binding attendedBinding
		if h.history != nil {
			c, err := h.history.connection(ctx, record, current.Target.Identity, enrollment)
			if err != nil {
				http.Error(w, "Attended authority unavailable; no transfer sent", http.StatusConflict)
				return
			}
			defer c.client.Close()
			stager, epoch, binding = c.client, c.epoch, c.binding
		} else {
			c, e, err := h.openDeploymentSession(ctx, record)
			if err != nil {
				http.Error(w, "Target unavailable; no transfer sent", 409)
				return
			}
			defer c.Close()
			stager, epoch = c, e
		}
		if prior := intent.PriorDeployment; prior != nil && (!restoreBindingMatches(prior, binding, "") || !h.history.authorizeRestore(ctx, session, current, prior.ObjectID) || !h.history.localRestoreIdle(ctx, binding.scope, current.Stack)) {
			http.Error(w, "Archived configuration is not authorized for review; no transfer sent", http.StatusConflict)
			return
		}
		stage, manifest, plan, err := stageAndPreview(ctx, stager, epoch, p.Stack, intent.Snapshot)
		if err != nil || (h.history == nil && (credentialRevision == "" || h.deploymentCredentialRevision(ctx, record) != credentialRevision)) {
			p.Error = "Staging or preview could not be confirmed. Inspect the receiver before preparing another transfer. No retry was sent."
			p.ID = ""
			h.render(w, p)
			return
		}
		var review *attendedReview
		if h.history != nil {
			p.Attended = true
			review = newAttendedReview(intent.Snapshot, binding, stage, manifest, p.Stack, plan)
			if review == nil || (intent.PriorDeployment != nil && !restoreBindingMatches(intent.PriorDeployment, binding, review.journal)) {
				p.ID = ""
				p.Error = "Cannot retain an exact attended review; no operation sent."
				break
			}
			review.prior = intent.PriorDeployment
		}
		h.renderPlan(&p, session, current.Target.Identity, stage, manifest, plan, review, credentialRevision)
	case deployPath + "/remove-preview":
		credentialRevision := h.deploymentCredentialRevision(ctx, record)
		if h.history != nil && h.history.operator != nil {
			http.Error(w, "Operator-configured authority is for new deployments only", http.StatusConflict)
			return
		}
		c, epoch, err := h.openDeploymentSession(ctx, record)
		if err != nil {
			http.Error(w, "Target unavailable", 409)
			return
		}
		defer c.Close()
		plan, err := c.PreviewRemoval(ctx, epoch, p.Stack)
		if err != nil || credentialRevision == "" || h.deploymentCredentialRevision(ctx, record) != credentialRevision {
			p.Error = "Managed removal preview unavailable. No operation was sent."
			break
		}
		h.renderPlan(&p, session, current.Target.Identity, "", "", plan, nil, credentialRevision)
	default:
		http.NotFound(w, r)
		return
	}
	h.render(w, p)
}

func (h *Deployment) renderPlan(p *deploymentPage, session, target, stage, manifest string, plan *agentclient.DeploymentPreview, attended *attendedReview, credentialRevision string) {
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
	p.ID = h.add(deploymentReview{Session: session, Host: p.Host, Stack: p.Stack, StageID: stage, Manifest: manifest, Selection: *plan.SelectionDigest, Mode: plan.Mode, PriorRequestID: plan.PriorRequestID, TargetDigest: target, CredentialRevision: credentialRevision, Journal: plan.JournalID, Expiry: time.Now().Add(5 * time.Minute), Attended: attended})
	if p.ID != "" {
		p.Mode = plan.Mode
	}
}

// New operator-authorized actions pin the active history enrollment and target
// under one lease. Legacy reads and removal retain their existing behavior.
func (h *Deployment) acquireDeploymentTarget(ctx context.Context, host string, attended bool) (targetregistry.Record, targetregistry.HistoryIncarnation, *targetregistry.ReadLease, error) {
	if h.credentials != nil || (attended && h.history != nil && h.history.operator != nil) {
		return h.registry.AcquireHistoryRead(ctx, host)
	}
	r, lease, err := h.registry.AcquireRead(ctx, host)
	return r, targetregistry.HistoryIncarnation{}, lease, err
}

func targetDigest(record targetregistry.Record) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(record.HTTPSAddress+"\x00"+string(record.HTTPSPairing))))
}

func (h *Deployment) openDeploymentSession(ctx context.Context, r targetregistry.Record, expected ...string) (*agentclient.Client, string, error) {
	if r.HTTPSAddress == "" {
		return nil, "", agentclient.ErrProtocol
	}
	p, err := agentclient.ParsePairing([]byte(r.HTTPSPairing))
	if err != nil {
		return nil, "", err
	}
	if h.credentials == nil {
		return nil, "", errDeployCredential
	}
	credential, e := h.credentials.Credential(ctx, r)
	if e != nil || (len(expected) > 0 && (expected[0] == "" || deploymentRevision(ctx, r, credential) != expected[0])) {
		return nil, "", errDeployCredential
	}
	return p.OpenDeploymentCurrent(ctx, ctx, r.HTTPSAddress, credential, func(ctx context.Context) bool {
		latest, err := h.credentials.Credential(ctx, r)
		return err == nil && subtle.ConstantTimeCompare([]byte(latest), []byte(credential)) == 1
	})
}

func stageAndPreview(ctx context.Context, c deploymentStager, epoch, stack string, snap projectsnapshot.Snapshot) (string, string, *agentclient.DeploymentPreview, error) {
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
	now := time.Now()
	for k, r := range h.pending {
		if !now.Before(r.Expiry) {
			h.dropReview(k)
		}
	}
	if h.closed || len(h.pending) >= 64 || (v.Attended != nil && v.Attended.bytes > maxAttendedReviewBytes-h.attendedBytes) {
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
	if a := v.Attended; a != nil {
		h.attendedBytes += a.bytes
		a.timer = time.AfterFunc(time.Until(v.Expiry), func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.pending[id].Attended == a {
				h.dropReview(id)
			}
		})
	}
	h.pending[id] = v
	return id
}

// dropReview requires h.mu. It releases retained byte accounting on expiry,
// consumption and shutdown, including when no subsequent request arrives.
func (h *Deployment) dropReview(id string) {
	if a := h.pending[id].Attended; a != nil {
		h.attendedBytes -= a.bytes
		a.timer.Stop()
	}
	delete(h.pending, id)
}

func (h *Deployment) take(id, session, confirm string) (deploymentReview, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.pending[id]
	if !ok || r.Session != session || confirm != r.Mode || !time.Now().Before(r.Expiry) {
		return deploymentReview{}, false
	}
	h.dropReview(id)
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
{{range .ManualIntents}}<p>Unresolved {{.Mode}} for <bdi>{{.Stack}}</bdi>: <a href="/remote/deployment?host={{.Host|urlquery}}&amp;id={{.Request|urlquery}}">Recover exact request {{.Request}}</a></p>{{end}}
{{if .ManualRecover}}<form method="post" action="/remote/deployment/recover"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="id" value="{{.ID}}"><label><input type="checkbox" name="confirm" value="check-and-fence" required> Check the original journal and prevent this request starting if not admitted. This does not cancel admitted operations or retry them.</label><button>Check and fence request</button></form>{{end}}
{{if .Error}}<p role="alert">{{.Error}}</p>{{end}}{{if .JournalCapacityLow}}<p role="alert">Dill's journal is nearing its size limit. Preserve the journal; do not delete it to make room.</p>{{end}}<p>Target: <bdi>{{.Host}}</bdi></p>
{{if and .ID (not .Review) (or (not .Attended) (not .Plan))}}<p><label for="deployment-request-id">Request ID</label><br><input type="text" id="deployment-request-id" value="{{.ID}}" readonly size="34" style="max-width:100%;box-sizing:border-box" aria-describedby="deployment-request-help"></p>
<p id="deployment-request-help">Select and copy the full request ID, or save the status link to return after a disconnect.</p>
{{if not .Attended}}<p><a href="/remote/deployment?host={{.Host|urlquery}}&amp;id={{.ID|urlquery}}">{{if .Plan}}Open status link{{else}}Refresh status{{end}}</a> · Checks this request without resubmitting it.</p>{{end}}{{end}}
{{if .Evidence}}<p role="status">{{.Evidence}}</p>{{end}}{{if .Status}}<p>Stack {{.Status.Stack}} · State {{.Status.State}} · Effects {{.Status.Effects}}{{if .Status.OperationID}} · Dill operation <code>{{.Status.OperationID}}</code>{{end}}</p>{{if eq .Status.State "unknown"}}<p role="alert">Outcome unknown. Dill recorded this request but could not determine its outcome. Inspect the target before taking further action; do not retry based on this result.</p>{{else if and (eq .Status.State "failed") (eq .Status.Effects "partial")}}<p role="alert">The operation failed after partially changing the target. Inspect the target and the recorded Dill operation before taking further action; do not retry based on this result.</p>{{end}}{{end}}
{{if .Review}}<h2>Review captured files</h2><p>Only transfer trusted projects. Staging may contain credentials; the configured target can evaluate Pkl with the agent account’s privileges. This is not a sandbox. Effective Brine variables are not transferred. No files have been sent yet.</p><p>Entrypoint: <code>{{.Review.Entrypoint}}</code></p><ul>{{range .Review.Files}}<li>{{.Path}} · {{.Size}} bytes · <code>{{printf "%x" .SHA256}}</code></li>{{end}}</ul>
<form method="post" action="/remote/deployment/stage"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="id" value="{{.Review.ID}}"><label><input type="checkbox" name="confirm" value="transfer" required> Send these exact files and request a preview</label><button>Stage and preview</button></form>
{{else if .Plan}}<h2>Deployment preview for {{.Stack}}</h2><ul>{{range .Plan.Resources}}<li>{{.Kind}} {{.Name}} · {{.Presence}} · {{.Action}}{{if .Retention}} · {{.Retention}}{{end}}</li>{{end}}</ul>
{{if .Mode}}<p>This preview did not permit writes.{{if not .Attended}} Save the status link before confirming if you need to recover after a disconnect.{{end}}</p><form method="post" action="/remote/deployment/create"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><label><input type="checkbox" name="confirm" value="{{.Mode}}" required> Confirm {{.Mode}} for this stack on this target</label><button>{{if eq .Mode "create"}}Create stack{{else if eq .Mode "update"}}Update stack{{else}}Remove containers; keep data{{end}}</button></form>{{else}}<p>Mutation unavailable. Existing resources are not adopted.</p>{{end}}
{{else if .GitPolicy}}<h2>Remote Git automation for {{.Stack}}</h2><p>Selected target: {{.Host}} · Branch: <code>{{.Branch}}</code> · State: {{.GitPolicy.Phase}} · Commit: <code>{{.GitPolicy.Commit}}</code></p>
{{if .GitPolicy.AttemptHistoryLow}}<p role="alert">{{if .GitPolicy.CanRecordAttempt}}The Git attempt history is nearing its size limit.{{else}}The Git attempt history is full. No new commits will be sent.{{end}} Preserve the recorded attempts.</p>{{end}}
{{with .GitPolicy.Reason}}<p>{{if eq . "awaiting-status"}}No terminal status has been recorded yet.{{else if eq . "running"}}Dill admitted the request and it is running.{{else if eq . "unknown"}}Dill admitted the request but cannot prove its outcome. Inspect the target; do not retry.{{else if eq . "status-unavailable"}}The exact request or its original journal could not be verified.{{else if eq . "staging-or-preview"}}Staging or preview did not complete. Inspect the receiver before taking further action.{{else if eq . "journal-capacity"}}Dill reports that its journal is near capacity. No apply was sent. Preserve the journal and inspect agent state.{{else if eq . "no-journal"}}The agent does not advertise a recoverable journal. No apply was sent.{{else if eq . "not-admitted"}}Dill fenced the request without admitting it. Automation is disabled and the same commit will not be resubmitted.{{else if eq . "failed"}}Dill reported a failure. Inspect the target before re-enabling automation.{{end}}</p>{{end}}
{{if .GitPolicy.Request}}<p><a href="/remote/deployment?host={{.GitPolicy.Host|urlquery}}&amp;id={{.GitPolicy.Request|urlquery}}">Look up pending request <code>{{.GitPolicy.Request}}</code></a>. A missing result is not evidence of no effects.</p>
{{if and .GitPolicy.Journal (eq .Target .GitPolicy.Target)}}<form method="post" action="/remote/deployment/git-config"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="target" value="{{.Target}}"><input type="hidden" name="request" value="{{.GitPolicy.Request}}"><label><input type="checkbox" name="confirm" value="fence-unadmitted" required> Forbid this exact request from ever starting if Dill has not admitted it. This cannot cancel an admitted operation.</label><button name="mode" value="recover">Check and fence request</button></form>{{else}}<p>Fencing is unavailable without the original target and a journal binding.</p>{{end}}{{end}}
<form method="post" action="/remote/deployment/git-config"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><input type="hidden" name="stack" value="{{.Stack}}"><input type="hidden" name="source" value="{{.Source}}"><input type="hidden" name="target" value="{{.Target}}">{{if eq .GitPolicy.Reason "source-sync-unconfirmed"}}{{if eq .Target .GitPolicy.Target}}<input type="hidden" name="source_sync" value="{{.GitPolicy.SourceSync}}"><p>Source synchronization is uncertain. Inspect the working tree and revision. This acknowledgement does not declare the sync successful and cannot retry or deploy it.</p><label><input type="checkbox" name="confirm" value="inspected-source-disable" required> I inspected this source sync; disable automation without retrying it.</label><button name="mode" value="recover-source">Confirm source inspection</button>{{else}}<p>Source inspection requires the original target binding. This target was replaced; no acknowledgement is available here.</p>{{end}}{{else if .GitPolicy.Enabled}}<button name="mode" value="disable">Disable remote auto-deploy</button>{{else if not .GitPolicy.Request}}<label><input type="checkbox" name="confirm" value="trusted" required> Authorize this Git branch to send exact commits and create or update this stack without individual review. The target evaluates trusted Pkl with agent privileges.</label><button name="mode" value="enable">Enable remote auto-deploy</button>{{end}}</form>
{{else if not .ID}}<form method="post" action="/remote/deployment/review"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Brine project name <input name="stack" required value="{{.Stack}}"></label><button>Review project files</button></form>
<form method="post" action="/remote/deployment/remove-preview"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Managed stack name <input name="stack" required value="{{.Stack}}"></label><button>Preview removal</button></form>
{{if .GitSettings}}<form method="post" action="/remote/deployment/git"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="host" value="{{.Host}}"><label>Git-backed Brine project <input name="stack" required></label><button>Remote auto-deploy settings</button></form>{{end}}{{end}}
</main></html>`))
