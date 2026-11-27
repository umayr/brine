package remoteui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

func (h *Deployment) EnableGit(r *git.Runner) {
	if st, ok := h.stacks.(store.RemoteGitStore); ok && r != nil {
		h.gitStore, h.git = st, r
	}
}

func (h *Deployment) gitSettings(ctx context.Context, w http.ResponseWriter, r *http.Request, p *deploymentPage, record targetregistry.Record) {
	if h.gitStore == nil {
		http.NotFound(w, r)
		return
	}
	releaseAdmission, admitted := h.gitAdmission.try(p.Stack)
	if !admitted {
		http.Error(w, "An automated or manual deployment is in progress", 409)
		return
	}
	defer releaseAdmission()
	if !h.manualClear(p.Stack) {
		http.Error(w, "Manual recovery is unresolved or unavailable", 409)
		return
	}
	stack, err := h.stacks.Get(p.Stack)
	source := stack.RemoteGitSource()
	if err != nil || source == "" {
		http.Error(w, "Choose a root-level Git-backed project with local auto-deploy disabled", 422)
		return
	}
	policy := stack.RemoteGit
	target := targetDigest(record)
	p.Branch, p.Source, p.Target = stack.RemoteGitBranch(), source, target
	if r.URL.Path == deployPath+"/git-config" {
		form := r.PostForm
		switch form.Get("mode") {
		case "recover":
			if policy == nil || policy.Host != p.Host || policy.Source != source || policy.Target != target || policy.Request == "" || policy.Journal == "" || form.Get("request") != policy.Request || form.Get("source") != source || form.Get("target") != target || form.Get("confirm") != "fence-unadmitted" {
				http.Error(w, "Pending request changed or cannot be fenced; review again", 409)
				return
			}
			p.ID = policy.Request
			ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			c, epoch, err := h.openDeploymentSession(ctx, record)
			if err != nil {
				p.Error = "Target unavailable. The pending request has not been cleared."
				break
			}
			defer c.Close()
			result, err := c.FenceDeployment(ctx, epoch, policy.Request, p.Stack, policy.Journal)
			if err != nil {
				p.Error = "No fence was verified. The pending request has not been cleared."
				if err == agentclient.ErrJournalCapacity {
					p.Error = "Dill could not record a fence because the journal is full. The pending request remains. Preserve the journal and inspect agent state."
				}
				break
			}
			p.Status = &result
			if result.State != "not-admitted" {
				p.Error = "Dill already admitted this request. It was not fenced or replayed."
				break
			}
			next := gitOutcome(stack, policy, result)
			if h.gitStore.SwapRemoteGit(p.Stack, source, policy, &next) != nil {
				p.Error = "Dill fenced the request, but Brine could not save the update. Look up the same request."
				break
			}
			policy = &next
		case "recover-source":
			if policy == nil || policy.Host != p.Host || policy.Source != source || policy.Target != target || policy.Reason != "source-sync-unconfirmed" || policy.Request != "" || form.Get("source") != source || form.Get("target") != target || form.Get("confirm") != "inspected-source-disable" || form.Get("source_sync") != policy.SourceSync || policy.SourceSync == "" {
				http.Error(w, "Explicit source inspection required; no operation retried", 409)
				return
			}
			next := *policy
			next.Enabled = false
			next.Phase, next.Reason = "disabled", ""
			next.SourceSync = ""
			if h.gitStore.SwapRemoteGit(p.Stack, source, policy, &next) != nil {
				http.Error(w, "Source state changed; inspect again", 409)
				return
			}
			policy = &next
		case "disable":
			if policy != nil && policy.Reason == "source-sync-unconfirmed" {
				http.Error(w, "Source sync outcome requires explicit inspection before disabling", 409)
				return
			}
			if policy == nil || policy.Host != p.Host || !policy.Enabled {
				http.Error(w, "Remote Git policy changed", 409)
				return
			}
			next := *policy
			next.Enabled = false
			if h.gitStore.SwapRemoteGit(p.Stack, source, policy, &next) != nil {
				http.Error(w, "Remote Git policy could not be saved", 409)
				return
			}
			policy = &next
		case "enable":
			if form.Get("confirm") != "trusted" || form.Get("source") != source || form.Get("target") != target || (policy != nil && (policy.Enabled || policy.Request != "")) {
				http.Error(w, "Remote Git authorization or policy changed; review again", 409)
				return
			}
			next := store.RemoteGitPolicy{Enabled: true, Host: p.Host, Target: target, Source: source, Phase: "ready"}
			if policy != nil && policy.Target == target && policy.Source == source {
				next.Commit, next.Seen = policy.Commit, policy.Seen
			}
			if h.gitStore.SwapRemoteGit(p.Stack, source, policy, &next) != nil {
				http.Error(w, "Remote Git policy could not be saved", 409)
				return
			}
			policy = &next
		default:
			http.Error(w, "Invalid remote Git action", 400)
			return
		}
	}
	if policy != nil && (policy.Enabled || policy.Request != "") && policy.Host != p.Host {
		http.Error(w, "This project is configured on another target", 409)
		return
	}
	if policy == nil {
		policy = &store.RemoteGitPolicy{Host: p.Host, Phase: "disabled"}
	}
	p.GitPolicy = policy
	h.render(w, *p)
}

// RunGit has one serialized poller, independent of local checkout and local
// deployment. A stalled result is queried, never submitted again.
func (h *Deployment) RunGit(ctx context.Context) {
	if h.gitStore == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		h.pollGit(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Deployment) pollGit(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	stacks, err := h.stacks.List()
	if err != nil {
		return
	}
	var workers sync.WaitGroup
	slots := make(chan struct{}, 4)
	defer workers.Wait()
	for _, s := range stacks {
		if ctx.Err() != nil {
			return
		}
		if s.RemoteGit != nil && (s.RemoteGit.Request != "" || (s.RemoteGit.Enabled && s.RemoteGit.Phase != "blocked")) {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			workers.Add(1)
			go func(name string) {
				defer workers.Done()
				defer func() { <-slots }()
				h.pollGitStack(ctx, name)
			}(s.Name)
		}
	}
}

type GitDeploymentResult struct {
	Submitted, Unchanged bool
	Error                error
	Status               agentclient.DeploymentStatus
}
type gitDeploymentGrant struct {
	binding, request string
	before           func() bool
}

func (h *Deployment) RunGitDeployment(ctx context.Context, name, binding, request string, before func() bool) GitDeploymentResult {
	if h.git == nil || h.gitStore == nil || h.credentials == nil || h.history != nil || before == nil {
		return GitDeploymentResult{}
	}
	return h.runGitStack(ctx, name, &gitDeploymentGrant{binding, request, before})
}
func (h *Deployment) pollGitStack(ctx context.Context, name string) {
	_ = h.runGitStack(ctx, name, nil)
}
func (h *Deployment) runGitStack(ctx context.Context, name string, grant *gitDeploymentGrant) (out GitDeploymentResult) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	releaseAdmission, admitted := h.gitAdmission.acquire(ctx, name)
	if !admitted {
		return
	}
	defer releaseAdmission()
	if !h.manualClear(name) {
		return
	}
	s, err := h.stacks.Get(name)
	if err != nil || s == nil || s.RemoteGit == nil {
		return
	}
	p := s.RemoteGit
	if s.RemoteGitSource() != p.Source || (!p.Enabled && p.Request == "") || p.Phase == "blocked" {
		return
	}
	record, enrollment, lease, err := h.acquireDeploymentTarget(ctx, p.Host, false)
	if err != nil {
		h.noteGitUnavailable(s, p)
		return
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
	revision := h.deploymentCredentialRevision(ctx, record)
	if grant != nil && (!lifecycleSource(s) || revision == "" || lifecycleBinding(s, record, enrollment, revision) != grant.binding) {
		return
	}
	if targetDigest(record) != p.Target {
		h.noteGitUnavailable(s, p)
		return
	}
	expected := []string{}
	if grant != nil {
		expected = append(expected, revision)
	}
	c, epoch, err := h.openDeploymentSession(ctx, record, expected...)
	if err != nil {
		h.noteGitUnavailable(s, p)
		return
	}
	defer c.Close()
	if p.Request != "" {
		result, err := c.JournalDeploymentStatus(ctx, epoch, p.Request, name, p.Journal)
		if err == nil {
			h.finishGit(s, p, result)
		} else {
			h.noteGitUnavailable(s, p)
		}
		return
	}
	if !p.CanRecordAttempt() {
		return
	}
	entry := s.Entrypoint
	if entry == "" {
		entry = "compose.pkl"
	}
	captureCtx, stopCapture := context.WithTimeout(ctx, 45*time.Second)
	commit, snap, err := h.git.CaptureBranch(captureCtx, s.Git.URL, s.RemoteGitBranch(), entry, captureLimits)
	stopCapture()
	if err != nil {
		return
	}
	seen := strings.Fields(p.Seen)
	if slices.Contains(seen, commit) {
		out.Unchanged = true
		return
	}
	// Remember that staging was attempted, including any ambiguous upload.
	staged := *p
	staged.Commit, staged.Phase, staged.Seen = commit, "blocked", strings.Join(append(seen, commit), " ")
	staged.Reason = "staging-or-preview"
	if h.gitStore.SwapRemoteGit(name, p.Source, p, &staged) != nil {
		return
	}
	stageCtx, stopStage := context.WithTimeout(ctx, 75*time.Second)
	stage, manifest, plan, err := stageAndPreview(stageCtx, c, epoch, name, snap)
	stopStage()
	if err != nil || plan.SelectionDigest == nil || (plan.Mode != "create" && plan.Mode != "update") {
		return
	}
	if grant != nil && (!qualifiedSourcePreview(plan, p) || h.deploymentCredentialRevision(ctx, record) != revision) {
		return
	}
	if plan.JournalID == "" || plan.JournalCapacityLow {
		next := staged
		next.Reason = "no-journal"
		if plan.JournalCapacityLow {
			next.Reason = "journal-capacity"
		}
		_ = h.gitStore.SwapRemoteGit(name, p.Source, &staged, &next)
		return
	}
	var request [16]byte
	if _, err := rand.Read(request[:]); err != nil {
		return
	}
	pending := staged
	pending.Request, pending.Phase, pending.Journal, pending.Reason = hex.EncodeToString(request[:]), "pending", plan.JournalID, "awaiting-status"
	if grant != nil {
		pending.Request = grant.request
	}
	if h.gitStore.SwapRemoteGit(name, p.Source, &staged, &pending) != nil {
		return
	}
	var result agentclient.DeploymentStatus
	if grant != nil {
		if !grant.before() {
			unsent := pending
			unsent.Request, unsent.Journal = "", ""
			unsent.Enabled = false
			unsent.Phase, unsent.Reason = "blocked", "not-admitted"
			_ = h.gitStore.SwapRemoteGit(name, p.Source, &pending, &unsent)
			return
		}
		out.Submitted = true
		result, err = c.ApplyCheckedJournalDeployment(ctx, epoch, stage, manifest, name, *plan.SelectionDigest, pending.Request, p.DeploymentRequest, pending.Journal)
	} else {
		out.Submitted = true
		result, err = c.ApplyJournalDeployment(ctx, epoch, stage, manifest, name, *plan.SelectionDigest, pending.Request, plan.Mode, pending.Journal)
	}
	if err == nil {
		out.Status = result
		out.Error = h.finishGit(s, &pending, result)
	}
	return
}

func (h *Deployment) noteGitUnavailable(s *store.Stack, p *store.RemoteGitPolicy) {
	if p.Request == "" || p.Reason == "status-unavailable" {
		return
	}
	next := *p
	next.Reason = "status-unavailable"
	_ = h.gitStore.SwapRemoteGit(s.Name, p.Source, p, &next)
}

func gitOutcome(s *store.Stack, p *store.RemoteGitPolicy, result agentclient.DeploymentStatus) store.RemoteGitPolicy {
	next := *p
	if result.RequestID != p.Request || result.Stack != s.Name {
		return next
	}
	switch result.State {
	case "running", "unknown":
		next.Reason = result.State
		return next
	case "succeeded":
		next.DeploymentRequest, next.DeploymentJournal = p.Request, p.Journal
		next.Phase, next.Reason = "succeeded", ""
	case "failed":
		next.Phase, next.Reason = "blocked", "failed"
	case "not-admitted":
		if p.Journal == "" {
			return next
		} // no original journal binding, no fence proof
		next.Phase, next.Reason, next.Enabled = "blocked", "not-admitted", false
	default:
		return next
	}
	next.Request, next.Journal = "", ""
	return next
}

func (h *Deployment) finishGit(s *store.Stack, p *store.RemoteGitPolicy, result agentclient.DeploymentStatus) error {
	next := gitOutcome(s, p, result)
	if next != *p {
		return h.gitStore.SwapRemoteGit(s.Name, p.Source, p, &next)
	}
	return nil
}
