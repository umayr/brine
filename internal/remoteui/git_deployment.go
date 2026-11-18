package remoteui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
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

func (h *Deployment) gitSettings(w http.ResponseWriter, r *http.Request, p *deploymentPage, record targetregistry.Record) {
	if h.gitStore == nil {
		http.NotFound(w, r)
		return
	}
	if !h.gitMu.TryLock() {
		http.Error(w, "An automated or manual deployment is in progress", 409)
		return
	}
	defer h.gitMu.Unlock()
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
		case "disable":
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
	for _, s := range stacks {
		if ctx.Err() != nil {
			return
		}
		if s.RemoteGit != nil && (s.RemoteGit.Request != "" || (s.RemoteGit.Enabled && s.RemoteGit.Phase != "blocked")) {
			h.pollGitStack(ctx, s.Name)
		}
	}
}

func (h *Deployment) pollGitStack(ctx context.Context, name string) {
	if !h.gitMu.TryLock() {
		return
	}
	defer h.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	s, err := h.stacks.Get(name)
	if err != nil || s == nil || s.RemoteGit == nil {
		return
	}
	p := s.RemoteGit
	if s.RemoteGitSource() != p.Source || (!p.Enabled && p.Request == "") || p.Phase == "blocked" {
		return
	}
	record, lease, err := h.registry.AcquireRead(ctx, p.Host)
	if err != nil {
		return
	}
	defer lease.Close()
	if targetDigest(record) != p.Target {
		return
	}
	c, epoch, err := openDeploymentSession(ctx, record)
	if err != nil {
		return
	}
	defer c.Close()
	if p.Request != "" {
		result, err := c.DeploymentStatus(ctx, epoch, p.Request)
		if err == nil && result.Stack == name {
			h.finishGit(s, p, result)
		}
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
	if slices.Contains(seen, commit) || len(seen) >= 128 {
		return
	}
	// Remember that staging was attempted, including any ambiguous upload.
	staged := *p
	staged.Commit, staged.Phase, staged.Seen = commit, "blocked", strings.Join(append(seen, commit), " ")
	if h.gitStore.SwapRemoteGit(name, p.Source, p, &staged) != nil {
		return
	}
	stageCtx, stopStage := context.WithTimeout(ctx, 75*time.Second)
	stage, manifest, plan, err := stageAndPreview(stageCtx, c, epoch, name, snap)
	stopStage()
	if err != nil || plan.SelectionDigest == nil || (plan.Mode != "create" && plan.Mode != "update") {
		return
	}
	var request [16]byte
	if _, err := rand.Read(request[:]); err != nil {
		return
	}
	pending := staged
	pending.Request, pending.Phase = hex.EncodeToString(request[:]), "pending"
	if h.gitStore.SwapRemoteGit(name, p.Source, &staged, &pending) != nil {
		return
	}
	result, err := c.ApplyDeployment(ctx, epoch, stage, manifest, name, *plan.SelectionDigest, pending.Request, plan.Mode)
	if err == nil {
		h.finishGit(s, &pending, result)
	}
}

func (h *Deployment) finishGit(s *store.Stack, p *store.RemoteGitPolicy, result agentclient.DeploymentStatus) {
	if result.RequestID != p.Request || result.Stack != s.Name {
		return
	}
	next := *p
	switch result.State {
	case "succeeded":
		next.Phase = "succeeded"
	case "failed":
		next.Phase = "blocked"
	default:
		return
	}
	next.Request = ""
	_ = h.gitStore.SwapRemoteGit(s.Name, p.Source, p, &next)
}
