package remoteui

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"testing"
)

type executionFixture struct {
	starts, finishes int
	deny             bool
	got              agentclient.DeploymentStatus
	sent             bool
}

func (o *executionFixture) Started() error {
	o.starts++
	if o.deny {
		return errors.New("journal unavailable")
	}
	return nil
}
func (o *executionFixture) Finished(s agentclient.DeploymentStatus, _ error, sent bool) {
	o.finishes++
	o.got = s
	o.sent = sent
}
func TestRemoteExecutionObservationDoesNotGrantReplayOrRewriteResults(t *testing.T) {
	h := &Deployment{}
	o := &executionFixture{}
	factories, calls := 0, 0
	h.SetExecutionObserver(func(context.Context, ExecutionMetadata) (ExecutionObservation, error) { factories++; return o, nil })
	status := agentclient.DeploymentStatus{RequestID: "synthetic", State: "failed", Effects: "partial"}
	rpcErr := errors.New("transport")
	run := func() (agentclient.DeploymentStatus, error) { calls++; return status, rpcErr }
	got, e := h.observeTarget(context.Background(), ExecutionMetadata{}, run)
	if got != status || e != rpcErr || calls != 1 || o.starts != 1 || o.finishes != 1 || !o.sent {
		t.Fatal("observer changed actual return or replayed")
	}
	o.deny = true
	h.observeTarget(context.Background(), ExecutionMetadata{}, run)
	if calls != 1 || o.sent {
		t.Fatal("journal denial executed target")
	}
	h.observeTarget(WithObservedExecution(context.Background()), ExecutionMetadata{}, run)
	if calls != 2 || factories != 2 {
		t.Fatal("incoming observation duplicated")
	}
}
