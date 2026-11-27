package server

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/webhooks"
)

type brokerExecutionObservation struct{ remoteExecutionObservation }

func (o *brokerExecutionObservation) Finished(status brokerapp.Outcome, err error, sent bool) {
	o.remoteExecutionObservation.Finished(agentclient.DeploymentStatus{RequestID: status.RequestID, Stack: status.Stack, State: status.State, Effects: status.Effects, OperationID: status.OperationID}, err, sent)
}
func (s *Server) newBrokerExecutionObservation(ctx context.Context, meta brokerapp.ExecutionMetadata) (brokerapp.ExecutionObservation, error) {
	if s.webhooks == nil || s.store == nil || (meta.Action != "plan" && meta.Action != "deploy") || meta.Stack == "" || meta.Target == "" || meta.TargetDigest == "" {
		return nil, webhooks.ErrInvalid
	}
	st, err := s.store.Get(meta.Stack)
	if err != nil || st == nil {
		return nil, webhooks.ErrInvalid
	}
	source := "workspace"
	if st.Git != nil {
		source = "git"
	}
	id := webhooks.NewOperationID()
	if meta.Request != "" {
		id = hookDigest("broker_op_", meta.TargetDigest+"\x00"+meta.Request)
	}
	op, err := s.webhooks.BeginOperation(ctx, webhooks.Event{ID: id, Stack: webhooks.Resource{ID: webhookStackID(st), Name: meta.Stack, Kind: "stack"}, Source: webhooks.Resource{Kind: source}, Target: webhooks.Resource{ID: hookDigest("broker-target_", meta.TargetDigest), Name: meta.Target, Kind: meta.TargetKind}, Action: meta.Action})
	if err != nil {
		return nil, err
	}
	return &brokerExecutionObservation{remoteExecutionObservation{ctx, op, meta.Action, meta.Request, meta.Stack}}, nil
}
