package server

import (
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"testing"
)

func TestRemoteLifecycleRecoveryFailureKeepsReportedOutcome(t *testing.T) {
	for _, state := range []string{"succeeded", "failed"} {
		s := agentclient.DeploymentStatus{RequestID: "target-request", State: state, Effects: "complete"}
		if state == "failed" {
			s.Effects = "none"
		}
		result, kind, _, effects, reason := remoteLifecycleResult("incoming", remoteui.LifecycleResult{Submitted: true, Status: s}, errors.New("recovery write"))
		if result.Status != "completed" || kind != "operation.completed" || effects != s.Effects || reason != "recovery_storage_unavailable" {
			t.Fatal("known outcome overwritten", result, kind, effects, reason)
		}
		if state == "succeeded" && result.Outcome != "success" || state == "failed" && result.Outcome != "failure" {
			t.Fatal(result)
		}
	}
	r, kind, _, effects, _ := remoteLifecycleResult("incoming", remoteui.LifecycleResult{Submitted: true}, errors.New("transport"))
	if r.Status != "unknown" || kind != "operation.outcome_unknown" || effects != "unknown" {
		t.Fatal("transport error inferred result")
	}
}
