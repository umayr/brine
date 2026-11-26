package remoteui

import (
	"context"
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
		if intents.ResolveManualIntent(intent) != nil {
			p.Error = "Verified terminal result, but recovery storage could not be updated. The stack remains blocked."
		}
	} else if !intent.Resolved {
		p.Error = "Outcome remains unresolved. Inspect the original target; no operation was retried."
	}
	h.render(w, *p)
}
