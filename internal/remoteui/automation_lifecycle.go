package remoteui

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
	"slices"
	"time"
)

var ErrAutomationAdmission = errors.New("remote automation admission unavailable")
var ErrAutomationRecovery = errors.New("remote automation preparation requires recovery")

// LifecycleTarget is a non-secret immutable automation ceiling. It is not a
// browser session or a credential. Enrollment/custody/source changes retire it.
type LifecycleTarget struct {
	ID, Name string
	Actions  []string
}
type LifecycleResult struct {
	Submitted bool
	Status    agentclient.DeploymentStatus
}

func lifecycleAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart"
}
func lifecycleSource(s *store.Stack) bool {
	return s != nil && s.RemoteGit != nil && s.RemoteGit.Enabled && s.RemoteGit.Request == "" && s.RemoteGit.Phase == "succeeded" && s.RemoteGit.DeploymentRequest != "" && s.RemoteGit.DeploymentJournal != "" && s.SourceHistory == nil && s.RemoteGitSource() == s.RemoteGit.Source
}
func lifecycleBinding(s *store.Stack, r targetregistry.Record, enrollment targetregistry.HistoryIncarnation, revision string) string {
	return fmt.Sprintf("remote_%x", sha256.Sum256([]byte(fmt.Sprintf("webhook-lifecycle/1\x00%s\x00%s\x00%s\x00%s\x00%v\x00%s", s.Name, s.CreatedAt.UTC().String(), s.RemoteGit.Source, targetDigest(r), enrollment, revision))))
}

// LifecycleTargets currently admits only an existing, successfully deployed,
// explicitly enabled remote Git owner. Other source workflows remain unavailable.
func (h *Deployment) LifecycleTargets(ctx context.Context, stack string) ([]LifecycleTarget, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return automationUnprotected(h, ctx, stack, func() ([]LifecycleTarget, error) { return h.lifecycleTargets(ctx, stack) })
}
func (h *Deployment) lifecycleTargets(ctx context.Context, stack string) ([]LifecycleTarget, error) {
	s, e := h.stacks.Get(stack)
	if e != nil || !lifecycleSource(s) || !h.manualClear(stack) || h.credentials == nil || h.gitStore == nil || h.history != nil {
		return nil, ErrAutomationAdmission
	}
	r, enrollment, lease, e := h.acquireDeploymentTarget(ctx, s.RemoteGit.Host, false)
	if e != nil {
		return nil, ErrAutomationAdmission
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, deploymentEnrollmentKey{}, enrollment)
	revision := h.deploymentCredentialRevision(ctx, r)
	if revision == "" || targetDigest(r) != s.RemoteGit.Target {
		return nil, ErrAutomationAdmission
	}
	c, epoch, e := h.openDeploymentSession(ctx, r, revision)
	if e != nil {
		return nil, ErrAutomationAdmission
	}
	defer c.Close()
	caps, e := c.Capabilities(ctx, epoch)
	if e != nil || !slices.Contains(caps, "deployment.journal") {
		return nil, ErrAutomationAdmission
	}
	actions := []string{}
	sourceOwnerCheck := h.git != nil && slices.Contains(caps, "deployment.stop-preview")
	if sourceOwnerCheck {
		actions = append(actions, "git-sync")
	}
	if slices.Contains(caps, "deployment.stage") && slices.Contains(caps, "deployment.manifest") && slices.Contains(caps, "deployment.preview") {
		if sourceOwnerCheck {
			actions = append([]string{"plan"}, actions...)
		}
		if slices.Contains(caps, "deployment.checked-apply") {
			actions = append(actions, "deploy")
		}
	}
	for _, a := range []string{"start", "stop", "restart"} {
		if slices.Contains(caps, "deployment."+a) && slices.Contains(caps, "deployment."+a+"-preview") {
			actions = append(actions, a)
		}
	}
	if len(actions) == 0 {
		return nil, ErrAutomationAdmission
	}
	return []LifecycleTarget{{ID: lifecycleBinding(s, r, enrollment, revision), Name: r.ID, Actions: actions}}, nil
}

// RunLifecycle does not invent a browser identity or consume an attended review.
// The caller presents its already-admitted immutable trigger ceiling;
// beforeDispatch records the final execution boundary after durable preparation. Durable recovery is shared with manual and Git paths.
func (h *Deployment) RunLifecycle(ctx context.Context, stack, binding, action, request string, beforeDispatch func() bool) (LifecycleResult, error) {
	var out LifecycleResult
	if !lifecycleAction(action) || beforeDispatch == nil || h.credentials == nil || h.gitStore == nil || h.history != nil {
		return out, ErrAutomationAdmission
	}
	release, ok := h.gitAdmission.acquire(ctx, stack)
	if !ok {
		return out, ErrAutomationAdmission
	}
	defer release()
	return automationUnprotected(h, ctx, stack, func() (LifecycleResult, error) {
		return h.runLifecycleAdmitted(ctx, stack, binding, action, request, beforeDispatch)
	})
}
func (h *Deployment) runLifecycleAdmitted(ctx context.Context, stack, binding, action, request string, beforeDispatch func() bool) (LifecycleResult, error) {
	var out LifecycleResult
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
	c, epoch, e := h.openDeploymentSession(ctx, r, revision)
	if e != nil {
		return out, ErrAutomationAdmission
	}
	defer c.Close()
	journal, e := c.DeploymentJournal(ctx, epoch)
	if e != nil {
		return out, ErrAutomationAdmission
	}
	p, e := c.PreviewLifecycle(ctx, epoch, stack, action, journal)
	if e != nil || p == nil || p.SelectionDigest == nil || p.JournalCapacityLow || journal != s.RemoteGit.DeploymentJournal || p.PriorRequestID != s.RemoteGit.DeploymentRequest {
		return out, ErrAutomationAdmission
	}
	fresh, e := h.stacks.Get(stack)
	if e != nil || !lifecycleSource(fresh) || *fresh.RemoteGit != *s.RemoteGit || !fresh.CreatedAt.Equal(s.CreatedAt) || h.deploymentCredentialRevision(ctx, r) != revision || ctx.Err() != nil {
		return out, ErrAutomationAdmission
	}
	intents, ok := h.stacks.(store.ManualDeploymentStore)
	intent := store.ManualDeploymentIntent{Host: r.ID, Target: targetDigest(r), Stack: stack, Mode: action, Request: request, PriorRequest: p.PriorRequestID, Journal: journal, Selection: *p.SelectionDigest}
	if !ok || intents.SaveManualIntent(intent) != nil {
		return out, ErrAutomationAdmission
	}
	if ctx.Err() != nil || !beforeDispatch() {
		if intents.ResolveManualIntent(intent) != nil {
			return out, ErrAutomationRecovery
		}
		return out, ErrAutomationAdmission
	}
	out.Submitted = true
	out.Status, e = c.ApplyLifecycle(ctx, epoch, stack, action, *p.SelectionDigest, request, p.PriorRequestID, journal)
	if e != nil {
		return out, e
	}
	if out.Status.State == "succeeded" {
		next := *s.RemoteGit
		next.DeploymentRequest = request
		next.DeploymentJournal = journal
		if h.gitStore.SwapRemoteGit(stack, s.RemoteGit.Source, s.RemoteGit, &next) != nil {
			return out, ErrAutomationAdmission
		}
	}
	if (out.Status.State == "succeeded" && (out.Status.Effects == "complete" || out.Status.Effects == "none")) || (out.Status.State == "failed" && out.Status.Effects == "none") {
		if intents.ResolveManualIntent(intent) != nil {
			return out, ErrAutomationAdmission
		}
	}
	return out, nil
}
