package server

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
	"log"
	"time"
)

type remoteExecutionObservation struct {
	ctx            context.Context
	op             *webhooks.Operation
	action         string
	request, stack string
}

func (o *remoteExecutionObservation) Started() error { return o.op.Started(o.ctx) }
func (o *remoteExecutionObservation) Finished(status agentclient.DeploymentStatus, e error, sent bool) {
	kind, outcome, effects, reason := "operation.rejected", "unavailable", "none", "admission_denied"
	if sent {
		kind, outcome, effects, reason = "operation.outcome_unknown", "unknown", "unknown", "execution_unconfirmed"
	}
	if sent && o.action == "plan" {
		effects = "none"
	}
	if sent && e == nil && o.action == "plan" {
		kind, outcome, effects, reason = "operation.completed", "success", "none", "qualified_preflight_result"
	}
	if status.RequestID != "" && status.RequestID == o.request && status.Stack == o.stack {
		switch status.State {
		case "succeeded":
			kind, outcome, effects, reason = "operation.completed", "success", status.Effects, "reported_target_result"
		case "failed":
			kind, outcome, effects, reason = "operation.completed", "failure", status.Effects, "reported_target_result"
		case "not-admitted":
			kind, outcome, effects, reason = "operation.rejected", "unavailable", "none", "verified_not_admitted"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if o.op.Finish(ctx, kind, outcome, effects, reason) != nil {
		log.Print("Remote execution outcome could not be journaled; preserve the exact target request and inspect before retrying")
	}
}
func (s *Server) newRemoteExecutionObservation(ctx context.Context, meta remoteui.ExecutionMetadata) (remoteui.ExecutionObservation, error) {
	if s.webhooks == nil || s.store == nil {
		return nil, webhooks.ErrInvalid
	}
	stackID := hookDigest("runtime-stack_", meta.Target+"\x00"+meta.Stack)
	source := "unavailable"
	var st *store.Stack
	var e error
	if meta.RuntimeOnly {
		e = store.ErrStackNotFound
	} else {
		st, e = s.store.Get(meta.Stack)
	}
	if e == nil && st == nil {
		return nil, webhooks.ErrInvalid
	}
	if e == nil {
		stackID = webhookStackID(st)
		source = "workspace"
		if st.Git != nil {
			source = "git"
		}
	} else if !errors.Is(e, store.ErrStackNotFound) {
		return nil, webhooks.ErrInvalid
	}
	id := hookDigest("remote_op_", meta.Target+"\x00"+meta.Request)
	if meta.Request == "" {
		id = webhooks.NewOperationID()
	}
	op, e := s.webhooks.BeginOperation(ctx, webhooks.Event{ID: id, Stack: webhooks.Resource{ID: stackID, Name: meta.Stack, Kind: "stack"}, Source: webhooks.Resource{Kind: source}, Target: webhooks.Resource{ID: "remote_" + meta.Target, Name: meta.Host, Kind: "remote"}, Action: meta.Action})
	if e != nil {
		return nil, e
	}
	return &remoteExecutionObservation{ctx, op, meta.Action, meta.Request, meta.Stack}, nil
}
