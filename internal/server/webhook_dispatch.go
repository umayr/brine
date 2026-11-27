package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/pkl"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
	"github.com/umayr/brine/internal/webhooks"
)

type webhookDispatcher struct{ s *Server }

func hookDigest(prefix, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return prefix + hex.EncodeToString(sum[:])
}
func webhookStackID(st *store.Stack) string {
	if st == nil || st.Name == "" || st.CreatedAt.IsZero() {
		return ""
	}
	return hookDigest("stack_", st.Name+"\x00"+st.CreatedAt.UTC().Format(time.RFC3339Nano))
}
func (d *webhookDispatcher) localTargetID() string {
	if d.s.agent == nil || d.s.agent.Dill() == nil {
		return ""
	}
	r := d.s.agent.Dill()
	if (r.Engine != "docker" && r.Engine != "podman") || !filepath.IsAbs(r.Socket) || filepath.Clean(r.Socket) != r.Socket {
		return ""
	}
	return hookDigest("local_", r.Engine+"\x00"+r.Socket)
}
func (d *webhookDispatcher) findStack(ctx context.Context, id string) (*store.Stack, error) {
	if ctx.Err() != nil || d.s.store == nil {
		return nil, webhooks.ErrUnsupported
	}
	stacks, e := d.s.store.List()
	if e != nil {
		return nil, webhooks.ErrUnsupported
	}
	for _, candidate := range stacks {
		if webhookStackID(candidate) == id {
			st, e := d.s.store.Get(candidate.Name)
			if e != nil || webhookStackID(st) != id {
				return nil, webhooks.ErrUnsupported
			}
			return st, nil
		}
	}
	return nil, webhooks.ErrUnsupported
}

// Validate grants no browser identity. Source, quarantine, protected-state and
// engine checks remain the runner's real gates, never a duplicate Dill planner.
func (d *webhookDispatcher) Validate(ctx context.Context, t webhooks.Trigger) error {
	if strings.HasPrefix(t.TargetID, "remote_") {
		return d.validateRemote(ctx, t)
	}
	if d.s.agent == nil || d.s.agent.Dill() == nil || t.TargetID == "" || t.TargetID != d.localTargetID() {
		return webhooks.ErrUnsupported
	}
	allowed := false
	for _, a := range webhooks.Actions {
		if a == t.Action {
			allowed = true
		}
	}
	if !allowed {
		return webhooks.ErrUnsupported
	}
	runner := d.s.agent.Dill()
	if runner.Profile == "alpha" || runner.QualifiedOperationConfigured() || runner.StrictActions != nil || runner.CheckSource == nil || runner.BeforeLegacyAction == nil || runner.ResolveStack == nil {
		return webhooks.ErrUnsupported
	}
	st, e := d.findStack(ctx, t.StackID)
	if e != nil {
		return e
	}
	// Disabling automation does not move its source to the local engine.
	if st.RemoteGit != nil {
		return webhooks.ErrUnsupported
	}
	if intents, ok := d.s.store.(store.ManualDeploymentStore); ok {
		rows, e := intents.ListManualIntents()
		if e != nil {
			return webhooks.ErrUnsupported
		}
		for _, row := range rows {
			// Resolution acknowledges inspection, not local deployment authority.
			// Retained remote request evidence has no safe implicit target handoff.
			if row.Stack == st.Name {
				return webhooks.ErrUnsupported
			}
		}
	} else {
		return webhooks.ErrUnsupported
	}
	name, e := runner.ResolveStack(st.PklPath)
	if e != nil || name != st.Name || runner.CheckSource(st.PklPath) != nil {
		return webhooks.ErrUnsupported
	}
	if runner.CheckSourceProtection != nil {
		if runner.CheckSourceProtection(st.Name) != nil {
			return webhooks.ErrUnsupported
		}
	} else if st.SourceHistory != nil {
		return webhooks.ErrUnsupported
	}
	content, e := d.s.store.ReadPkl(st.Name)
	if e != nil {
		return webhooks.ErrUnsupported
	}
	declared := pkl.DeclaredEngine(content)
	if declared != "" && declared != runner.Engine {
		return webhooks.ErrUnsupported
	}
	if runner.BeforeLegacyAction(ctx, st.PklPath) != nil {
		return webhooks.ErrUnsupported
	}
	if t.Action == "git-sync" && (st.Git == nil || d.s.git == nil) {
		return webhooks.ErrUnsupported
	}
	return nil
}
func (d *webhookDispatcher) WebhookOptions(ctx context.Context) ([]web.WebhookOption, error) {
	out := []web.WebhookOption{}
	if d.s.store == nil {
		return out, webhooks.ErrUnsupported
	}
	stacks, e := d.s.store.List()
	if e != nil {
		return nil, webhooks.ErrUnsupported
	}
	for _, st := range stacks {
		if ctx.Err() != nil {
			return nil, webhooks.ErrUnsupported
		}
		sid, tid := webhookStackID(st), d.localTargetID()
		out = append(out, d.remoteOptions(ctx, sid, st.Name)...)
		if sid == "" || tid == "" {
			continue
		}
		actions := []string{}
		if d.Validate(ctx, webhooks.Trigger{StackID: sid, TargetID: tid, Action: "plan"}) == nil {
			for _, a := range webhooks.Actions {
				if a != "git-sync" || (st.Git != nil && d.s.git != nil) {
					actions = append(actions, a)
				}
			}
		}
		if len(actions) > 0 {
			out = append(out, web.WebhookOption{StackID: sid, StackName: st.Name, TargetID: tid, TargetName: "Local / " + d.s.cfg.Engine, Actions: actions})
		}
	}
	return out, nil
}
func (d *webhookDispatcher) Execute(ctx context.Context, t webhooks.Trigger, request webhooks.IncomingRequest) webhooks.TriggerResult {
	if strings.HasPrefix(t.TargetID, "remote_") {
		return d.executeRemote(ctx, t, request)
	}
	result := webhooks.TriggerResult{Status: "rejected", Reason: "admission_denied", OperationID: request.ID}
	source := "unknown"
	stackName := ""
	initialStack, lookupErr := d.findStack(ctx, t.StackID)
	if st, e := initialStack, lookupErr; e == nil {
		stackName = st.Name
		source = "workspace"
		if st.Git != nil {
			source = "git"
		}
	}
	op, e := d.s.webhooks.BeginOperation(ctx, webhooks.Event{ID: request.ID, Stack: webhooks.Resource{ID: t.StackID, Name: stackName, Kind: "stack"}, Source: webhooks.Resource{Kind: source}, Target: webhooks.Resource{ID: t.TargetID, Kind: "local"}, Action: t.Action})
	if e != nil {
		if errors.Is(e, webhooks.ErrCapacity) || errors.Is(e, webhooks.ErrInvalid) {
			return webhooks.TriggerResult{Status: "rejected", Reason: "operation_journal_capacity", OperationID: request.ID}
		}
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	}
	finish := func(kind, outcome, effects, reason string) {
		saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if op.Finish(saveCtx, kind, outcome, effects, reason) != nil {
			result = webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
		}
	}
	if lookupErr != nil || stackName == "" || d.Validate(ctx, t) != nil {
		finish("operation.rejected", "unavailable", "none", "admission_denied")
		return result
	}
	runner := d.s.agent.Dill()
	admission, cancel := context.WithTimeout(ctx, 30*time.Second)
	unlock, e := runner.LockStackContext(admission, stackName)
	cancel()
	if e != nil {
		finish("operation.rejected", "unavailable", "none", "stack_busy")
		return result
	}
	defer unlock()
	// Identity/source can change while queued; recheck under the same lock used by
	// API and UI lifecycle paths, then invoke their existing runner methods.
	if d.Validate(ctx, t) != nil {
		finish("operation.rejected", "unavailable", "none", "admission_denied")
		return result
	}
	st, e := d.findStack(ctx, t.StackID)
	if e != nil || st.Name != stackName {
		finish("operation.rejected", "unavailable", "none", "source_unavailable")
		return result
	}
	if t.Action == "deploy" && d.s.cfg.StagingSocket != "" {
		result.Status = "awaiting_review"
		result.Reason = "interactive_review_required"
		finish("operation.rejected", "unavailable", "none", "interactive_review_required")
		return result
	}
	if t.Action != "git-sync" && op.Started(ctx) != nil {
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	}
	ctx = context.WithValue(ctx, webhookObservedOperationKey{}, true)
	switch t.Action {
	case "plan":
		_, e = runner.Plan(ctx, st.PklPath)
	case "deploy":
		e = runner.Up(ctx, st.PklPath, io.Discard)
	case "start":
		e = runner.Start(ctx, st.PklPath, io.Discard)
	case "stop":
		e = runner.Stop(ctx, st.PklPath, io.Discard)
	case "restart":
		e = runner.Restart(ctx, st.PklPath, io.Discard)
	case "git-sync":
		repo, repoErr := d.s.store.RepoDir(st.Name)
		if repoErr != nil {
			result.Reason = "source_unavailable"
			finish("operation.rejected", "unavailable", "none", result.Reason)
			return result
		}
		observed := &webhookLifecycleObserver{op: op, ctx: ctx}
		pullCtx := gitpkg.WithPullObserver(ctx, &webhookPullObserver{observed})
		_, _, e = gitpkg.PullAndRecord(pullCtx, d.s.git, d.s.store, st.Name, repo)
		if observed.finished {
			return observed.result
		}
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	default:
		e = webhooks.ErrUnsupported
	}
	if e != nil {
		result.Status = "unknown"
		result.Outcome = "unknown"
		result.Reason = "execution_unconfirmed"
		finish("operation.outcome_unknown", "unknown", "unknown", "execution_unconfirmed")
		return result
	}
	result.Status = "completed"
	result.Outcome = "success"
	result.Reason = ""
	finish("operation.completed", "success", "unknown", "")
	return result
}

// Keep compilation tied to the existing Dill boundary; no alternative executor
// or shell-command API is introduced by this adapter.
var _ webhooks.TriggerDispatcher = (*webhookDispatcher)(nil)
