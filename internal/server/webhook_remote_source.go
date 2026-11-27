package server

import (
	"context"
	"github.com/umayr/brine/internal/webhooks"
	"time"
)

func remoteOperationTarget(t webhooks.Trigger) webhooks.Resource {
	if t.Action == "git-sync" {
		return webhooks.Resource{Kind: "none"}
	}
	return webhooks.Resource{ID: t.TargetID, Kind: "remote"}
}
func (d *webhookDispatcher) executeRemoteSource(ctx context.Context, t webhooks.Trigger, request webhooks.IncomingRequest, op *webhooks.Operation, stack string) webhooks.TriggerResult {
	out, e := d.s.remoteDeploy.RunSourceAction(ctx, stack, t.TargetID, t.Action, func() bool { return op.Started(ctx) == nil })
	result := webhooks.TriggerResult{Status: "rejected", Reason: "admission_denied", OperationID: request.ID}
	kind, outcome, effects := "operation.rejected", "unavailable", "none"
	if out.Attempted {
		result.Status, result.Outcome, result.Reason = "unknown", "unknown", "execution_unconfirmed"
		kind, outcome, effects = "operation.outcome_unknown", "unknown", out.Effects
		if out.Outcome == "success" {
			result.Status, result.Outcome, result.Reason = "completed", "success", ""
			kind, outcome, effects = "operation.completed", "success", out.Effects
			if e != nil {
				result.Reason = "recovery_storage_unavailable"
			}
		}
	}
	save, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var err error
	if out.Changed != nil {
		err = op.FinishWithChanges(save, kind, outcome, effects, result.Reason, *out.Changed)
	} else {
		err = op.Finish(save, kind, outcome, effects, result.Reason)
	}
	if err != nil {
		return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown", Reason: "operation_history_unavailable", OperationID: request.ID}
	}
	return result
}
