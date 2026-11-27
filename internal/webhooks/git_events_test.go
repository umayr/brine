package webhooks

import (
	"context"
	"testing"
)

func TestGitDerivedEventsRetainReportedChangeAndUnknownAxes(t *testing.T) {
	for _, kind := range []string{"operation.completed", "operation.outcome_unknown"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s := testService(t, 10)
			ep, _ := testEndpoint(t, s)
			ep.Events = []string{"git.sync.started", "git.sync.completed", "git.sync.failed", "git.sync.outcome_unknown"}
			s.SaveEndpoint(ctx, ep)
			event := testEvent("op_git")
			event.Action = "git-sync"
			op, e := s.BeginOperation(ctx, event)
			if e != nil {
				t.Fatal(e)
			}
			op.Started(ctx)
			if kind == "operation.completed" {
				op.FinishWithChanges(ctx, kind, "success", "unknown", "", false)
			} else {
				op.Finish(ctx, kind, "unknown", "unknown", "execution_unconfirmed")
			}
			if e = s.reconcileOperations(ctx); e != nil {
				t.Fatal(e)
			}
			s.reconcileOperations(ctx)
			deliveries, e := s.Deliveries(ctx, ep.ID)
			if e != nil || len(deliveries) != 2 {
				t.Fatalf("missing/duplicated sync events: %+v %v", deliveries, e)
			}
			terminalFound := false
			for _, d := range deliveries {
				if d.Event.Type == "git.sync.completed" {
					terminalFound = true
					if d.Event.ChangesDetected == nil || *d.Event.ChangesDetected {
						t.Fatal("false changed value lost")
					}
				}
				if d.Event.Type == "git.sync.failed" {
					t.Fatal("uncertain sync mislabeled as failed")
				}
				if d.Event.Type == "git.sync.outcome_unknown" {
					terminalFound = true
					if d.Event.Outcome != "unknown" || d.Event.Effects != "unknown" || d.Event.ChangesDetected != nil {
						t.Fatal("unknown source effects inferred as unchanged")
					}
				}
			}
			if !terminalFound {
				t.Fatal("terminal sync event absent")
			}
		})
	}
}
