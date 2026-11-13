package dill

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// These constraints come from pinned b3 Recorder.Begin/Planned/Intent/Finish
// and runOperation's post-Finish close/commit handling, not desired-state guesses.
func TestOperationResourceAggregateCoherence(t *testing.T) {
	observed := func(effect string) OperationResource {
		return OperationResource{Kind: "container", IdentityDigest: operationDigest("observed"), Action: "remove", Effect: effect}
	}
	planned := OperationResource{Kind: "service", IdentityDigest: operationDigest("planned"), Action: "noop", Effect: "none"}
	for _, test := range []struct {
		name, op, outcome, effects string
		resources                  []OperationResource
		truncated                  bool
		code                       string
		exit                       int
		valid                      bool
	}{
		{name: "validate-noop", op: "validate", outcome: "succeeded", effects: "none", valid: true},
		{name: "mutation-noop", op: "stop", outcome: "succeeded", effects: "none", valid: true},
		{name: "readonly-plan", op: "plan", outcome: "succeeded", effects: "none", resources: []OperationResource{planned}, valid: true},
		{name: "readonly-diff", op: "diff", outcome: "succeeded", effects: "none", resources: []OperationResource{planned}, valid: true},
		{name: "dry-up-planned", op: "up", outcome: "succeeded", effects: "none", resources: []OperationResource{planned}, valid: true},
		{name: "complete", op: "stop", outcome: "succeeded", effects: "complete", resources: []OperationResource{observed("complete")}, valid: true},
		{name: "partial", op: "down", outcome: "failed", effects: "partial", resources: []OperationResource{observed("complete")}, code: "OPERATION_FAILED", exit: 1, valid: true},
		{name: "cancelled-partial", op: "down", outcome: "cancelled", effects: "partial", resources: []OperationResource{observed("complete")}, code: "CANCELLED", exit: 1, valid: true},
		{name: "invalid-input-partial", op: "down", outcome: "invalid_input", effects: "partial", resources: []OperationResource{observed("complete")}, code: "INVALID_INPUT", exit: 2, valid: true},
		{name: "unknown-visible-unknown", op: "down", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("unknown")}, code: "OPERATION_FAILED", exit: 1, valid: true},
		{name: "close-unknown-visible-complete", op: "down", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("complete")}, code: "ENGINE_CLOSE_FAILED", exit: 1, valid: true},
		{name: "commit-unknown-visible-complete", op: "down", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("complete")}, code: "COORDINATION_COMMIT_FAILED", exit: 1, valid: true},
		{name: "readonly-close-unknown-empty", op: "plan", outcome: "unknown", effects: "unknown", code: "ENGINE_CLOSE_FAILED", exit: 1, valid: true},
		{name: "readonly-close-unknown-planned", op: "plan", outcome: "unknown", effects: "unknown", resources: []OperationResource{planned}, code: "ENGINE_CLOSE_FAILED", exit: 1, valid: true},
		{name: "truncated-unknown-visible-complete", op: "down", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("complete")}, truncated: true, code: "OPERATION_FAILED", exit: 1, valid: true},
		{name: "truncated-complete", op: "stop", outcome: "succeeded", effects: "complete", resources: []OperationResource{observed("complete")}, truncated: true, valid: true},
		{name: "truncated-partial", op: "down", outcome: "failed", effects: "partial", resources: []OperationResource{observed("complete")}, truncated: true, code: "OPERATION_FAILED", exit: 1, valid: true},
		{name: "cleanup-failure-known-axes", op: "stop", outcome: "succeeded", effects: "complete", resources: []OperationResource{observed("complete")}, code: "COORDINATION_RELEASE_FAILED", exit: 1, valid: true},
		{name: "validate-contradictory-success", op: "validate", outcome: "succeeded", effects: "none", resources: []OperationResource{observed("unknown")}},
		{name: "mutation-contradictory-success-unknown", op: "up", outcome: "succeeded", effects: "none", resources: []OperationResource{observed("unknown")}},
		{name: "mutation-contradictory-success-complete", op: "up", outcome: "succeeded", effects: "none", resources: []OperationResource{observed("complete")}},
		{name: "partial-visible-unknown", op: "down", outcome: "failed", effects: "partial", resources: []OperationResource{observed("unknown")}, code: "OPERATION_FAILED", exit: 1},
		{name: "truncated-known-visible-unknown", op: "down", outcome: "failed", effects: "partial", resources: []OperationResource{observed("unknown")}, truncated: true, code: "OPERATION_FAILED", exit: 1},
		{name: "truncated-none-visible-complete", op: "up", outcome: "succeeded", effects: "none", resources: []OperationResource{observed("complete")}, truncated: true},
		{name: "mutation-none-observation", op: "up", outcome: "succeeded", effects: "none", resources: []OperationResource{observed("none")}},
		{name: "validate-planned-observation", op: "validate", outcome: "succeeded", effects: "none", resources: []OperationResource{planned}},
		{name: "validate-conservative-unknown-empty", op: "validate", outcome: "unknown", effects: "unknown", code: "ENGINE_CLOSE_FAILED", exit: 1, valid: true},
		{name: "readonly-plan-mutation", op: "plan", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("unknown")}, code: "ENGINE_CLOSE_FAILED", exit: 1},
		{name: "readonly-diff-mutation", op: "diff", outcome: "unknown", effects: "unknown", resources: []OperationResource{observed("complete")}, code: "ENGINE_CLOSE_FAILED", exit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := consumerReceipt(test.op)
			r.Outcome = test.outcome
			r.Effects = test.effects
			r.ResourcesTruncated = test.truncated
			if test.resources != nil {
				r.Resources = test.resources
			}
			if test.code != "" {
				r.Errors = []OperationDiagnostic{{Code: test.code, Message: operationMessages[test.code]}}
			}
			decoded, err := parseOperationReceipt(consumerJSON(t, r))
			if !test.valid {
				if err == nil || decoded != nil {
					t.Fatal("accepted producer-impossible resource/aggregate combination")
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected valid producer axes: %v", err)
			}
			if !validOperationExit(decoded, test.exit) {
				t.Fatal("valid producer exit rejected")
			}
			if decoded.Outcome != r.Outcome || decoded.Effects != r.Effects || decoded.ResourcesTruncated != r.ResourcesTruncated {
				t.Fatal("known axes rewritten")
			}
			if test.code == "COORDINATION_RELEASE_FAILED" && invocationStatus(decoded, test.exit) != "failed" {
				t.Fatal("cleanup failure became success")
			}
		})
	}
}

func TestOperationContradictorySuccessProcessFailsClosed(t *testing.T) {
	rcp := consumerReceipt("validate")
	rcp.Resources = []OperationResource{{Kind: "container", IdentityDigest: operationDigest("x"), Action: "remove", Effect: "unknown"}}
	r, log := consumerScript(t, consumerDiscovery(false), append(consumerJSON(t, rcp), '\n'), 0, "")
	var out bytes.Buffer
	terminal, err := r.operation(context.Background(), "/synthetic/config.pkl", "validate", &out)
	if err == nil || terminal.Status != "unknown" || terminal.Receipt != nil {
		t.Fatalf("contradictory success trusted: status=%s err=%v", terminal.Status, err)
	}
	if strings.Count(out.String(), OperationMarker) != 1 {
		t.Fatal("missing single conservative terminal")
	}
	decoded, e := DecodeOperationInvocation([]byte(strings.TrimSuffix(strings.TrimPrefix(out.String(), OperationMarker), "\n")))
	if e != nil || decoded.Receipt != nil || decoded.Status != "unknown" {
		t.Fatal("invalid conservative marker")
	}
	if data, e := os.ReadFile(log); e != nil || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("test did not reach discovery+action: %s %v", data, e)
	}
}
