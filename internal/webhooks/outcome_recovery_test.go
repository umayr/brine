package webhooks

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPlanRecoveryNeverInventsRuntimeEffects(t *testing.T) {
	s := testService(t, 10)
	ctx := context.Background()
	event := testEvent("op_plan_interrupted")
	event.Action = "plan"
	op, err := s.BeginOperation(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Started(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.recoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var terminal Event
	if err = s.db.QueryRow("SELECT terminal FROM lifecycle_records WHERE id=?", event.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &terminal) != nil || terminal.Outcome != "unknown" || terminal.Effects != "none" {
		t.Fatal("Plan gained runtime effects", terminal)
	}
}
func TestUnknownQueueDispositionPreservesReceivedOutcome(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{execute: func(Trigger, IncomingRequest) TriggerResult {
		return TriggerResult{Status: "unknown", Outcome: "failure", Reason: "operation_history_unavailable"}
	}}
	r, err := acceptRequest(s, tr, key, `{"id":"received-but-history-unavailable"}`, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.executeOne(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Requests(context.Background(), tr.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != r.ID || rows[0].Status != "unknown" || rows[0].Outcome != "failure" {
		t.Fatal("received failure became unknown", rows, err)
	}
	if _, err = acceptRequest(s, tr, key, `{"id":"new-request"}`, d); err != ErrBusy {
		t.Fatal("known outcome released unknown queue fence", err)
	}
}
