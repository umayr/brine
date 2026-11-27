package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/web"
	"github.com/umayr/brine/internal/webhooks"
	"slices"
	"time"
)

func (d *webhookDispatcher) remoteOptions(ctx context.Context, stackID, stack string) []web.WebhookOption {
	if d.s.remoteDeploy == nil {
		return nil
	}
	targets, e := d.s.remoteDeploy.LifecycleTargets(ctx, stack)
	if e != nil {
		return nil
	}
	out := []web.WebhookOption{}
	for _, target := range targets {
		out = append(out, web.WebhookOption{StackID: stackID, StackName: stack, TargetID: target.ID, TargetName: target.Name, Actions: target.Actions})
	}
	return out
}
func (d *webhookDispatcher) validateRemote(ctx context.Context, t webhooks.Trigger) error {
	if t.Action != "start" && t.Action != "stop" && t.Action != "restart" && t.Action != "plan" && t.Action != "git-sync" && t.Action != "deploy" {
		return webhooks.ErrUnsupported
	}
	s, e := d.findStack(ctx, t.StackID)
	if e != nil {
		return e
	}
	for _, option := range d.remoteOptions(ctx, t.StackID, s.Name) {
		if option.TargetID == t.TargetID && slices.Contains(option.Actions, t.Action) {
			return nil
		}
	}
	return webhooks.ErrUnsupported
}
func (d *webhookDispatcher) executeRemote(ctx context.Context, t webhooks.Trigger, request webhooks.IncomingRequest) webhooks.TriggerResult {
	result := webhooks.TriggerResult{Status: "rejected", Reason: "admission_denied", OperationID: request.ID}
	s, e := d.findStack(ctx, t.StackID)
	if e != nil || d.validateRemote(ctx, t) != nil {
		return result
	}
	op, e := d.s.webhooks.BeginOperation(ctx, webhooks.Event{ID: request.ID, Stack: webhooks.Resource{ID: t.StackID, Name: s.Name, Kind: "stack"}, Source: webhooks.Resource{Kind: "git"}, Target: remoteOperationTarget(t), Action: t.Action})
	if e != nil {
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if t.Action == "plan" || t.Action == "git-sync" {
		return d.executeRemoteSource(ctx, t, request, op, s.Name)
	}
	ctx = remoteui.WithObservedExecution(ctx)
	id := sha256.Sum256([]byte("brine-webhook-target/1\x00" + request.ID))
	var out remoteui.LifecycleResult
	if t.Action == "deploy" {
		deployed := d.s.remoteDeploy.RunGitDeployment(ctx, s.Name, t.TargetID, fmt.Sprintf("%x", id[:16]), func() bool { return op.Started(ctx) == nil })
		out = remoteui.LifecycleResult{Submitted: deployed.Submitted, Status: deployed.Status}
		e = deployed.Error
		if deployed.Unchanged {
			save, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if op.Finish(save, "operation.rejected", "unavailable", "none", "revision_previously_attempted") != nil {
				return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
			}
			return webhooks.TriggerResult{Status: "rejected", Outcome: "unavailable", Reason: "revision_previously_attempted", OperationID: request.ID}
		}
	} else {
		out, e = d.s.remoteDeploy.RunLifecycle(ctx, s.Name, t.TargetID, t.Action, fmt.Sprintf("%x", id[:16]), func() bool { return op.Started(ctx) == nil })
	}
	result, kind, outcome, effects, reason := remoteLifecycleResult(request.ID, out, e)
	save, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if op.Finish(save, kind, outcome, effects, reason) != nil {
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	}
	return result
}
func remoteLifecycleResult(id string, out remoteui.LifecycleResult, e error) (webhooks.TriggerResult, string, string, string, string) {
	result := webhooks.TriggerResult{Status: "rejected", Reason: "admission_denied", OperationID: id}
	kind, outcome, effects, reason := "operation.rejected", "unavailable", "none", "admission_denied"
	if !out.Submitted && errors.Is(e, remoteui.ErrAutomationRecovery) {
		result.Status, result.Outcome, result.Reason = "unknown", "unknown", "recovery_storage_unavailable"
		kind, outcome, effects, reason = "operation.outcome_unknown", "unknown", "none", result.Reason
	}
	if out.Submitted {
		result.Status = "unknown"
		result.Outcome = "unknown"
		result.Reason = "execution_unconfirmed"
		kind, outcome, effects, reason = "operation.outcome_unknown", "unknown", "unknown", "execution_unconfirmed"
		if out.Status.RequestID != "" {
			switch out.Status.State {
			case "succeeded":
				result.Status = "completed"
				result.Outcome = "success"
				result.Reason = ""
				kind, outcome, effects, reason = "operation.completed", "success", out.Status.Effects, ""
			case "not-admitted":
				result.Status, result.Outcome, result.Reason = "rejected", "unavailable", "target_not_admitted"
				kind, outcome, effects, reason = "operation.rejected", "unavailable", "none", "target_not_admitted"
			case "failed":
				result.Status = "completed"
				result.Outcome = "failure"
				result.Reason = "execution_failed"
				kind, outcome, effects, reason = "operation.completed", "failure", out.Status.Effects, "execution_failed"
			}
		}
	}
	if e != nil && result.Status == "completed" {
		result.Reason = "recovery_storage_unavailable"
		reason = result.Reason
	}
	return result, kind, outcome, effects, reason
}
