package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/store"
	"html/template"
	"net/http"
	"sort"
)

type brokerRequestKey struct{}

func (h *Handler) brokerSession(r *http.Request) string {
	if h.auth == nil || !auth.CookieAuthenticated(r) {
		return ""
	}
	// Request authentication metadata alone can outlive logout or session expiry.
	// Revalidate the live session at every target/policy gate, including callbacks.
	live, release, ok := h.auth.SessionContext(r)
	if !ok {
		return ""
	}
	defer release()
	if live.Err() != nil {
		return ""
	}
	s := h.stagingSession(r)
	if s == "" {
		return ""
	}
	return brokerwire.Digest([]byte("brine-broker-session/1:" + s))
}
func (h *Handler) brokerCapture(ctx context.Context, name string) (projectsnapshot.Snapshot, string, error) {
	var empty projectsnapshot.Snapshot
	capture, ok := h.store.(interface {
		ProjectSnapshot(string, projectsnapshot.Limits) (*store.Stack, projectsnapshot.Snapshot, error)
	})
	if !ok {
		return empty, "", brokerapp.ErrUnavailable
	}
	stack, snap, err := capture.ProjectSnapshot(name, previewLimits)
	if err != nil || stack == nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	if h.agent == nil || h.agent.Dill() == nil || h.agent.Dill().Variables == nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	vars, err := h.agent.Dill().Variables.Effective(name)
	if err != nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	inputs, err := brokerwire.EncodeInputs(vars)
	if err != nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	const identityFile = ".brine-broker-source-identity"
	for _, f := range snap.Files {
		if f.Path == identityFile || f.Path == brokerwire.InputsFile {
			return empty, "", brokerapp.ErrUnavailable
		}
	}
	metadata, err := json.Marshal(stack)
	if err != nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	content := []byte(brokerwire.Digest(metadata))
	sum := sha256.Sum256(content)
	snap.Files = append(snap.Files, projectsnapshot.File{Path: identityFile, Content: content, SHA256: sum}, projectsnapshot.File{Path: brokerwire.InputsFile, Content: inputs, SHA256: sha256.Sum256(inputs)})
	sort.Slice(snap.Files, func(i, j int) bool { return snap.Files[i].Path < snap.Files[j].Path })
	if projectsnapshot.ValidateProvisionalReceiver(ctx, snap) != nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	digest, err := dillserve.SnapshotManifestDigest(ctx, snap)
	if err != nil {
		return empty, "", brokerapp.ErrUnavailable
	}
	return snap, "sha256:" + hex.EncodeToString(digest[:]), nil
}
func (h *Handler) brokerGuard(ctx context.Context, i secretbroker.ReviewIdentity) (func(), error) {
	r, ok := ctx.Value(brokerRequestKey{}).(*http.Request)
	if !ok || h.brokerSession(r) != i.SessionDigest || !h.broker.Enabled() || i.PolicyRevision != h.broker.Revision() {
		return nil, brokerapp.ErrUnavailable
	}
	allowed := false
	for _, s := range h.broker.Scopes(i.Scope.StackName) {
		if s == i.Scope {
			allowed = true
		}
	}
	if !allowed {
		return nil, brokerapp.ErrUnavailable
	}
	unlock, err := h.agent.Dill().LockStackContext(ctx, i.Scope.StackName)
	if err != nil {
		return nil, brokerapp.ErrUnavailable
	}
	vars, err := h.agent.Dill().Variables.ReadLease(ctx)
	if err != nil {
		unlock()
		return nil, brokerapp.ErrUnavailable
	}
	_, digest, err := h.brokerCapture(ctx, i.Scope.StackName)
	if err != nil || digest != i.SourceDigest {
		vars()
		unlock()
		return nil, brokerapp.ErrUnavailable
	}
	lease, release, ok := h.auth.SubmissionLease(r.WithContext(ctx))
	if !ok || lease.Err() != nil {
		if ok {
			release()
		}
		vars()
		unlock()
		return nil, brokerapp.ErrUnavailable
	}
	return func() { release(); vars(); unlock() }, nil
}

var brokerIntro = template.Must(template.New("broker-intro").Parse(`<section class="ui-panel p-5 space-y-3"><h2 class="font-semibold">Plan · source and secret preparation</h2><p>Source: {{.Stack}}. Review this complete captured file list before transfer. Hidden and ignored files may contain inline credentials; only .git entries are omitted. Other files, including non-Pkl files, will be sent to the selected execution target. Pkl is operator-trusted code, not sandboxed. Git sources use the current Brine checkout, not an automatic branch fetch. Source and execution target are independent.</p><p>Non-secret effective configuration variables are included in the captured input document; their values are not displayed here. Variable names: {{range .Variables}}<code>{{.}}</code> {{else}}none{{end}}.</p><p>No deployment has been submitted. Preparation resolves static references centrally and stages private files on the target; source rotation does not change running containers.</p><ul class="source-file-list">{{range .Files}}<li><code>{{.Path}}</code> · {{len .Content}} bytes · <code class="break-all">{{printf "%x" .SHA256}}</code></li>{{end}}</ul><form data-broker-plan method="post" action="/web/stacks/{{.Stack}}/broker/prepare" hx-post="/web/stacks/{{.Stack}}/broker/prepare" hx-target="#plan-result" hx-swap="innerHTML" class="space-y-3"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="source" value="{{.Digest}}"><label class="ui-field"><span>Execution target</span><select class="ui-input" name="registration">{{range .Scopes}}<option value="{{.RegistrationID}}">{{.TargetRegistration}}</option>{{end}}</select></label><button class="ui-button ui-button-primary" name="confirm" value="prepare">Prepare references and Plan</button></form></section>`))

func (h *Handler) brokerPlanIntro(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	name := chi.URLParam(r, "name")
	if !h.broker.Enabled() || h.brokerController == nil || h.brokerSession(r) == "" {
		h.renderAlert(w, "error", "Secrets require current browser authentication and configured operator policy.")
		return
	}
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	defer unlock()
	snap, digest, err := h.brokerCapture(r.Context(), name)
	scopes := h.broker.Scopes(name)
	if err != nil || len(scopes) == 0 {
		h.renderAlert(w, "error", "Source capture unavailable. This broker profile requires a complete trusted project and bounded configuration inputs; an authorized workspace must be available; nothing was transferred.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = brokerIntro.Execute(w, struct {
		Stack, CSRF, Digest string
		Variables           []string
		Files               []projectsnapshot.File
		Scopes              []secretbroker.Scope
	}{name, h.auth.CSRFFor(r), digest, brokerInputNames(snap), snap.Files, scopes})
}
func brokerForm(r *http.Request, w http.ResponseWriter, keys ...string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.URL.RawQuery != "" || r.ParseForm() != nil {
		return false
	}
	allowed := map[string]bool{"_csrf": true}
	for _, k := range keys {
		allowed[k] = true
	}
	for k, v := range r.PostForm {
		if !allowed[k] || len(v) != 1 {
			return false
		}
	}
	return true
}
func encodeBrokerSource(s projectsnapshot.Snapshot) ([]byte, error) {
	files := []map[string]any{}
	for _, f := range s.Files {
		files = append(files, map[string]any{"path": f.Path, "bytes": base64.StdEncoding.EncodeToString(f.Content), "length": len(f.Content), "sha256": hex.EncodeToString(f.SHA256[:])})
	}
	return json.Marshal(map[string]any{"schema": "dill.snapshot.v1", "entrypoint": s.Entrypoint, "files": files})
}

type brokerStager interface {
	Stage(context.Context, []byte) (string, error)
	Snapshot(context.Context, string) (dillserve.Manifest, error)
}

func (h *Handler) brokerPrepare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.broker.Enabled() || h.brokerController == nil || h.brokerSession(r) == "" {
		http.Error(w, "Broker unavailable", 403)
		return
	}
	if !brokerForm(r, w, "source", "registration", "confirm") || r.PostForm.Get("confirm") != "prepare" {
		http.Error(w, "Invalid preparation", 400)
		return
	}
	name := chi.URLParam(r, "name")
	var scope secretbroker.Scope
	for _, s := range h.broker.Scopes(name) {
		if s.RegistrationID == r.PostForm.Get("registration") {
			scope = s
		}
	}
	if scope.RegistrationID == "" {
		http.Error(w, "Target scope unavailable", 403)
		return
	}
	unlock, ok := h.admitStack(w, r, name)
	if !ok {
		return
	}
	vars, err := h.agent.Dill().Variables.ReadLease(r.Context())
	if err != nil {
		unlock()
		http.Error(w, "Variables unavailable", 409)
		return
	}
	snap, digest, err := h.brokerCapture(r.Context(), name)
	if err != nil || digest != r.PostForm.Get("source") {
		vars()
		unlock()
		http.Error(w, "Source changed or unavailable; Plan again", 409)
		return
	}
	ctx, release, ok := h.auth.SubmissionLease(r)
	if !ok {
		vars()
		unlock()
		http.Error(w, "Session unavailable", 403)
		return
	}
	target, closeTarget, err := h.openBrokerTarget(r.Context(), scope.TargetRegistration, func(c context.Context) bool { return c.Err() == nil && h.brokerSession(r) != "" })
	if err != nil {
		release()
		vars()
		unlock()
		http.Error(w, "Approved target unavailable", 409)
		return
	}
	defer closeTarget()
	stageTarget, ok := target.(brokerStager)
	if !ok {
		release()
		vars()
		unlock()
		http.Error(w, "Staging unavailable", 409)
		return
	}
	body, err := encodeBrokerSource(snap)
	if err != nil {
		release()
		vars()
		unlock()
		http.Error(w, "Source unavailable", 409)
		return
	}
	stage, err := stageTarget.Stage(ctx, body)
	clear(body)
	if err == nil {
		var manifest dillserve.Manifest
		manifest, err = stageTarget.Snapshot(ctx, stage)
		d := manifest.Digest()
		if "sha256:"+hex.EncodeToString(d[:]) != digest {
			err = brokerapp.ErrUnavailable
		}
	}
	release()
	vars()
	unlock()
	if err != nil {
		http.Error(w, "Source transfer outcome unavailable; inspect target before retrying", 409)
		return
	}
	snapshot, err := h.broker.State.Load(scope)
	if err != nil {
		http.Error(w, "Binding state unavailable", 409)
		return
	}
	info, err := target.Info(r.Context())
	if err != nil {
		http.Error(w, "Target identity unavailable", 409)
		return
	}
	identity := secretbroker.ReviewIdentity{Scope: scope, SourceDigest: digest, TargetDigest: info.TargetDigest, PolicyRevision: h.broker.Revision(), SessionDigest: h.brokerSession(r), BindingRevision: snapshot.Revision}
	ctx = context.WithValue(r.Context(), brokerRequestKey{}, r)
	review, err := h.brokerController.Prepare(ctx, identity, stage, digest[7:], target)
	if err != nil {
		http.Error(w, "Secret preparation or native Plan unavailable; no deployment submitted", 409)
		return
	}
	h.renderBrokerReview(w, r, review)
}

var brokerConfirm = template.Must(template.New("broker-confirm").Parse(`<section class="ui-panel p-5 space-y-3"><h2 class="font-semibold">{{if .Review.Retirement}}Review secret retirement{{else}}Review secret delivery{{end}}</h2><p>Target: {{.Review.Identity.Scope.TargetRegistration}} · binding revision {{.Review.Identity.BindingRevision}} · generation <code>{{.Review.Reference.Scope.Generation}}</code></p>{{if .Review.Retirement}}<p>Retire secret mounts from retained services. Omitted secret workloads block this Plan. Historical generations, volumes, provider values and protection records are retained; nothing is pruned.</p>{{end}}<p>{{if .Review.Retirement}}An empty generation is prepared, not deployed.{{else}}Files are prepared, not deployed.{{end}} Approval expires {{.Review.ExpiresAt}}. Values are not shown.</p><ul>{{range .Review.Requirements.Grants}}<li>{{.Service}} / {{.Name}} → <code>{{.Target}}</code> · UID {{.UID}}, GID {{.GID}}, mode {{printf "%04o" .Mode}}</li>{{end}}</ul><form data-broker-plan hx-post="/web/stacks/{{.Review.Identity.Scope.StackName}}/broker/confirm" hx-target="#plan-result" hx-swap="innerHTML" method="post" action="/web/stacks/{{.Review.Identity.Scope.StackName}}/broker/confirm"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="review" value="{{.Review.ID}}"><button class="ui-button ui-button-primary" name="confirm" value="deploy">Deploy reviewed Plan</button></form></section>`))

func (h *Handler) renderBrokerReview(w http.ResponseWriter, r *http.Request, review brokerapp.PlanReview) {
	result := &dill.PlanResult{Changes: review.Native.Plan.Changes}
	for _, c := range result.Changes {
		switch c.Kind {
		case "create":
			result.Summary.Create++
		case "recreate":
			result.Summary.Recreate++
		case "remove":
			result.Summary.Remove++
		case "noop":
			result.Summary.Noop++
		}
	}
	h.renderPartial(w, r, "plan-diff", result)
	_ = brokerConfirm.Execute(w, struct {
		Review brokerapp.PlanReview
		CSRF   string
	}{review, h.auth.CSRFFor(r)})
}
func (h *Handler) brokerConfirmPlan(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.broker.Enabled() || h.brokerController == nil || h.brokerSession(r) == "" {
		http.Error(w, "Broker unavailable", 403)
		return
	}
	if !brokerForm(r, w, "review", "confirm") || r.PostForm.Get("confirm") != "deploy" {
		http.Error(w, "Invalid confirmation", 400)
		return
	}
	review, err := h.brokerController.Inspect(r.PostForm.Get("review"), chi.URLParam(r, "name"), h.brokerSession(r))
	if err != nil {
		http.Error(w, "Review expired or unavailable; Plan again", 409)
		return
	}
	target, closeTarget, err := h.openBrokerTarget(r.Context(), review.Identity.Scope.TargetRegistration, func(ctx context.Context) bool {
		return ctx.Err() == nil && h.brokerSession(r) == review.Identity.SessionDigest
	})
	if err != nil {
		http.Error(w, "Approved target unavailable", 409)
		return
	}
	defer closeTarget()
	ctx := context.WithValue(r.Context(), brokerRequestKey{}, r)
	record, err := h.brokerController.Submit(ctx, review.ID, review.Identity, target)
	if record.Intent.ID == "" {
		http.Error(w, "Not submitted; source/session/bindings changed or unavailable", 409)
		return
	}
	if len(record.Events) > 0 && record.Events[len(record.Events)-1].State == "not-admitted" {
		http.Error(w, "Not admitted; inputs changed or unavailable. No deployment submitted. Inspect History, then Plan again.", http.StatusConflict)
		return
	}
	if err != nil {
		h.renderAlert(w, "warning", "Deployment is not confirmed successful. Inspect the same request in History; do not replay it.")
	} else {
		last := record.Events[len(record.Events)-1]
		level := "info"
		if last.State == "succeeded" {
			level = "success"
		} else if last.State == "failed" {
			level = "error"
		}
		h.renderAlert(w, level, "Target reported "+last.State+"; effects: "+last.Effects+". Immutable metadata is in History.")
	}
}
