package server

import (
	"github.com/umayr/brine/internal/remoteui"
	"testing"
)

func TestRemotePreparationCleanupFailureIsNotOrdinaryDenial(t *testing.T) {
	result, kind, outcome, effects, reason := remoteLifecycleResult("request", remoteui.LifecycleResult{}, remoteui.ErrAutomationRecovery)
	if result.Status != "unknown" || result.Outcome != "unknown" || result.Reason != "recovery_storage_unavailable" || kind != "operation.outcome_unknown" || outcome != "unknown" || effects != "none" || reason != result.Reason {
		t.Fatal("failed preparation recovery hidden", result, kind, outcome, effects, reason)
	}
}
