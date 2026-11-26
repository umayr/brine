package remoteui

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/store"
	"net/http"
	"time"
)

func (h *Deployment) manualClear(stack string) bool {
	intents, ok := h.stacks.(store.ManualDeploymentStore)
	if !ok {
		return false
	}
	rows, err := intents.ListManualIntents()
	if err != nil {
		return false
	}
	for _, r := range rows {
		if !r.Resolved && r.Stack == stack {
			return false
		}
	}
	return true
}

// Called only after interactive authentication, CSRF and submission lease.
// Fencing cannot submit or replay an engine operation.
func (h *Deployment) recoverManual(ctx context.Context, w http.ResponseWriter, r *http.Request, p *deploymentPage) {
	if r.PostForm.Get("confirm") != "check-and-fence" {
		http.Error(w, "Explicit recovery confirmation required", 400)
		return
	}
	intents, ok := h.stacks.(store.ManualDeploymentStore)
	if !ok {
		http.Error(w, deploymentStatusUnavailable, 409)
		return
	}
	intent, err := intents.GetManualIntent(p.ID)
	if err != nil || intent.Host != p.Host {
		http.Error(w, deploymentStatusUnavailable, 409)
		return
	}
	release, ok := h.gitAdmission.try(intent.Stack)
	if !ok {
		http.Error(w, "Deployment busy", 409)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	record, enrollment, recordLease, err := h.acquireDeploymentTarget(ctx, intent.Host, false)
	if err != nil {
		http.Error(w, deploymentStatusUnavailable, 409)
		return
	}
	defer recordLease.Close()
	ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
	if targetDigest(record) != intent.Target {
		http.Error(w, deploymentStatusUnavailable, 409)
		return
	}
	c, epoch, err := h.openDeploymentSession(ctx, record)
	if err != nil {
		http.Error(w, deploymentStatusUnavailable, 409)
		return
	}
	defer c.Close()
	result, err := c.FenceDeployment(ctx, epoch, intent.Request, intent.Stack, intent.Journal)
	p.Stack = intent.Stack
	if err != nil {
		p.Error = deploymentStatusUnavailable
		h.render(w, *p)
		return
	}
	p.Status = &result
	if !intent.Resolved && (result.State == "succeeded" || result.State == "failed" || result.State == "not-admitted") {
		if !h.reconcileLifecycleIntent(intent, result) {
			p.Error = "Verified target result, but the reviewed deployment head could not be reconciled. The stack remains blocked."
			h.render(w, *p)
			return
		}
		if intents.ResolveManualIntent(intent) != nil {
			p.Error = "Verified terminal result, but recovery storage could not be updated. The stack remains blocked."
		}
	} else if !intent.Resolved {
		p.Error = "Outcome remains unresolved. Inspect the original target; no operation was retried."
	}
	h.render(w, *p)
}

// Only the authenticated inspection path may reconcile an uncertain lifecycle
// against the saved reviewed predecessor. It never submits a target operation.
func (h *Deployment) reconcileLifecycleIntent(intent store.ManualDeploymentIntent, status agentclient.DeploymentStatus) bool {
	if !lifecycleAction(intent.Mode) {
		return true
	}
	if status.State == "not-admitted" {
		return status.Action == ""
	}
	if status.Action != intent.Mode {
		return false
	}
	if status.State != "succeeded" {
		return status.State == "failed" && status.Effects == "none"
	}
	if h.gitStore == nil {
		return false
	}
	s, e := h.stacks.Get(intent.Stack)
	if e != nil || !lifecycleSource(s) || s.RemoteGit.Host != intent.Host || s.RemoteGit.Target != intent.Target || s.RemoteGit.DeploymentJournal != intent.Journal {
		return false
	}
	if s.RemoteGit.DeploymentRequest == intent.Request {
		return true
	}
	if s.RemoteGit.DeploymentRequest != intent.PriorRequest {
		return false
	}
	next := *s.RemoteGit
	next.DeploymentRequest = intent.Request
	return h.gitStore.SwapRemoteGit(s.Name, s.RemoteGit.Source, s.RemoteGit, &next) == nil
}
