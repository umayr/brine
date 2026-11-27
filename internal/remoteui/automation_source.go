package remoteui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/umayr/brine/internal/agentclient"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"time"
)

type SourceResult struct {
	Attempted        bool
	Outcome, Effects string
	Changed          *bool
}
type reviewedPull struct {
	runner         *gitpkg.Runner
	remote, branch string
}

func (p reviewedPull) Pull(ctx context.Context, dir string) (bool, error) {
	return p.runner.PullBranch(ctx, dir, p.remote, p.branch)
}
func (p reviewedPull) ShortRef(ctx context.Context, dir string) (string, error) {
	return p.runner.ShortRefReviewed(ctx, dir)
}

type sourceObservation struct {
	before func() bool
	out    *SourceResult
}

func (o sourceObservation) Started() error {
	if !o.before() {
		return ErrAutomationAdmission
	}
	o.out.Attempted = true
	return nil
}
func (o sourceObservation) Finished(changed bool, e error) {
	if !o.out.Attempted {
		return
	}
	o.out.Outcome, o.out.Effects = "unknown", "unknown"
	if e == nil {
		o.out.Outcome, o.out.Effects = "success", "none"
		o.out.Changed = &changed
	}
}

// RunSourceAction acts only for an already reviewed remote Git owner. Sync is
// source-only: no Dill execution or deployment. Plan stages/previews but NEVER
// applies. Unknown source mutation blocks the shared policy until inspected.
func (h *Deployment) RunSourceAction(ctx context.Context, stack, binding, action string, beforeDispatch func() bool) (SourceResult, error) {
	var out SourceResult
	if (action != "plan" && action != "git-sync") || beforeDispatch == nil || h.git == nil || h.gitStore == nil || h.credentials == nil || h.history != nil {
		return out, ErrAutomationAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	release, ok := h.gitAdmission.acquire(ctx, stack)
	if !ok {
		return out, ErrAutomationAdmission
	}
	defer release()
	s, e := h.stacks.Get(stack)
	if e != nil || !lifecycleSource(s) || !h.manualClear(stack) {
		return out, ErrAutomationAdmission
	}
	r, enrollment, lease, e := h.acquireDeploymentTarget(ctx, s.RemoteGit.Host, false)
	if e != nil {
		return out, ErrAutomationAdmission
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
	revision := h.deploymentCredentialRevision(ctx, r)
	if revision == "" || targetDigest(r) != s.RemoteGit.Target || lifecycleBinding(s, r, enrollment, revision) != binding {
		return out, ErrAutomationAdmission
	}
	current := func() bool {
		fresh, e := h.stacks.Get(stack)
		return e == nil && lifecycleSource(fresh) && fresh.CreatedAt.Equal(s.CreatedAt) && *fresh.RemoteGit == *s.RemoteGit && h.deploymentCredentialRevision(ctx, r) == revision && ctx.Err() == nil
	}
	if action == "git-sync" {
		repo, e := h.stacks.RepoDir(stack)
		if e != nil {
			return out, ErrAutomationAdmission
		}
		var incident [16]byte
		if _, e := rand.Read(incident[:]); e != nil {
			return out, ErrAutomationAdmission
		}
		blocked := *s.RemoteGit
		blocked.SourceSync = hex.EncodeToString(incident[:])
		blocked.Phase, blocked.Reason = "blocked", "source-sync-unconfirmed"
		prepared := false
		observation := sourceObservation{out: &out, before: func() bool {
			if !current() || h.gitStore.SwapRemoteGit(stack, s.RemoteGit.Source, s.RemoteGit, &blocked) != nil {
				return false
			}
			prepared = true
			if !beforeDispatch() {
				_ = h.gitStore.SwapRemoteGit(stack, s.RemoteGit.Source, &blocked, s.RemoteGit)
				return false
			}
			return true
		}}
		pullCtx := gitpkg.WithPullObserver(ctx, observation)
		_, _, e = gitpkg.PullAndRecord(pullCtx, reviewedPull{h.git, s.Git.URL, s.RemoteGitBranch()}, h.stacks, stack, repo)
		if out.Outcome == "success" && prepared {
			if h.gitStore.SwapRemoteGit(stack, s.RemoteGit.Source, &blocked, s.RemoteGit) != nil {
				return out, ErrAutomationAdmission
			}
		}
		return out, e
	}
	entry := s.Entrypoint
	if entry == "" {
		entry = "compose.pkl"
	}
	_, snap, e := h.git.CaptureBranch(ctx, s.Git.URL, s.RemoteGitBranch(), entry, captureLimits)
	if e != nil || !current() {
		return out, ErrAutomationAdmission
	}
	c, epoch, e := h.openDeploymentSession(ctx, r, revision)
	if e != nil {
		return out, ErrAutomationAdmission
	}
	defer c.Close()
	if !beforeDispatch() {
		return out, ErrAutomationAdmission
	}
	out.Attempted = true
	out.Outcome, out.Effects = "unknown", "none"
	_, _, p, e := stageAndPreview(ctx, c, epoch, stack, snap)
	if e != nil {
		return out, e
	}
	if !qualifiedSourcePreview(p, s.RemoteGit) || !current() {
		return out, ErrAutomationAdmission
	}
	out.Outcome, out.Effects = "success", "none"
	return out, nil
}
func qualifiedSourcePreview(p *agentclient.DeploymentPreview, owner *store.RemoteGitPolicy) bool {
	return p != nil && p.Mode == "update" && p.SelectionDigest != nil && p.JournalID == owner.DeploymentJournal && p.PriorRequestID == owner.DeploymentRequest
}
