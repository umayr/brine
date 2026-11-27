package server

import (
	"context"
	"github.com/umayr/brine/internal/dill"
	"testing"
)

func TestReceivedOutcomeSurvivesTerminalJournalFailure(t *testing.T) {
	s, fs, _, _ := dispatcherServer(t)
	stack, err := fs.Get("example")
	if err != nil {
		t.Fatal(err)
	}
	observer, err := s.newWebhookObservation(context.Background(), stack.PklPath, "deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.Started(); err != nil {
		t.Fatal(err)
	}
	s.webhooks.Close()
	observer.Finished(&dill.OperationError{Invocation: dill.OperationInvocation{Receipt: &dill.OperationReceipt{Outcome: "failure", Effects: "partial"}}})
	if observer.result.Status != "unknown" || observer.result.Outcome != "failure" || observer.result.Reason != "operation_history_unavailable" {
		t.Fatal("received fact overwritten", observer.result)
	}
}
