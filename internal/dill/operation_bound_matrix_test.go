package dill

import (
	"encoding/json"
	"strings"
	"testing"
)

// Literals deliberately do not consult either production diagnostic catalog.
func TestBoundInvocationAdversarialMatrix(t *testing.T) {
	codes := []OperationDiagnostic{
		{Code: "PREFLIGHT_MISMATCH", Message: "Reviewed resource selection changed. Review again before retrying."},
		{Code: "PREFLIGHT_UNAVAILABLE", Message: "Complete preflight evidence unavailable."},
	}
	closeDiag := OperationDiagnostic{Code: "ENGINE_CLOSE_FAILED", Message: "Engine connection release failed. Inspect the target before retrying."}
	commitDiag := OperationDiagnostic{Code: "COORDINATION_COMMIT_FAILED", Message: "Durable outcome unavailable. Inspect coordination state before retrying."}
	releaseDiag := OperationDiagnostic{Code: "COORDINATION_RELEASE_FAILED", Message: "Engine outcome was recorded; local coordination cleanup failed."}
	other := OperationDiagnostic{Code: "INVALID_INPUT", Message: "Invalid operation input."}
	base := func(code OperationDiagnostic, action string) OperationInvocation {
		return OperationInvocation{ExitCode: 1, Status: "failed", Receipt: &OperationReceipt{
			Version: "dill.operation/1", OperationID: "op_00000000000000000000000000000009", Operation: action,
			Target: &OperationTarget{Engine: "docker", EndpointDigest: "sha256:" + strings.Repeat("1", 64), QualifiedIdentityDigest: "sha256:" + strings.Repeat("2", 64)},
			Stack:  "synthetic", Outcome: "failed", Effects: "none", Resources: []OperationResource{}, Errors: []OperationDiagnostic{code},
		}}
	}
	check := func(t *testing.T, v OperationInvocation, accept bool) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		_, e = DecodeBoundOperationInvocation(b)
		if (e == nil) != accept {
			t.Fatalf("accept=%v error=%v input=%s", accept, e, b)
		}
		if _, e = DecodeOperationInvocation(b); e == nil {
			t.Fatalf("old decoder accepted %s", b)
		}
	}
	for _, code := range codes {
		for _, action := range []string{"up", "start", "restart", "stop", "down"} {
			t.Run(code.Code+"/"+action, func(t *testing.T) {
				check(t, base(code, action), true)
				cases := []struct {
					name   string
					mutate func(*OperationInvocation)
				}{
					{"wrong-message", func(v *OperationInvocation) { v.Receipt.Errors[0].Message = "wrong" }},
					{"exit-zero", func(v *OperationInvocation) { v.ExitCode = 0 }},
					{"exit-two", func(v *OperationInvocation) { v.ExitCode = 2 }},
					{"missing-process", func(v *OperationInvocation) { v.ExitCode = -1 }},
					{"wrong-status", func(v *OperationInvocation) { v.Status = "succeeded" }},
					{"invalid-input", func(v *OperationInvocation) { v.Receipt.Outcome = "invalid_input"; v.ExitCode = 2 }},
					{"readonly", func(v *OperationInvocation) { v.Receipt.Operation = "plan" }},
					{"empty-action", func(v *OperationInvocation) { v.Receipt.Operation = "" }},
					{"success", func(v *OperationInvocation) { v.Receipt.Outcome = "succeeded"; v.ExitCode = 0; v.Status = "succeeded" }},
					{"partial", func(v *OperationInvocation) { v.Receipt.Effects = "partial" }},
					{"complete", func(v *OperationInvocation) { v.Receipt.Effects = "complete" }},
					{"unknown-no-cleanup", func(v *OperationInvocation) {
						v.Receipt.Outcome = "unknown"
						v.Receipt.Effects = "unknown"
						v.Status = "unknown"
					}},
					{"duplicate-primary", func(v *OperationInvocation) { v.Receipt.Errors = append(v.Receipt.Errors, code) }},
					{"other-primary-after", func(v *OperationInvocation) { v.Receipt.Errors = append(v.Receipt.Errors, other) }},
					{"other-primary-before", func(v *OperationInvocation) { v.Receipt.Errors = append([]OperationDiagnostic{other}, code) }},
					{"cleanup-before-primary", func(v *OperationInvocation) { v.Receipt.Errors = append([]OperationDiagnostic{closeDiag}, code) }},
					{"duplicate-cleanup", func(v *OperationInvocation) { v.Receipt.Errors = append(v.Receipt.Errors, closeDiag, closeDiag) }},
					{"wrong-cleanup-order", func(v *OperationInvocation) { v.Receipt.Errors = append(v.Receipt.Errors, releaseDiag, commitDiag) }},
					{"malformed-target-digest", func(v *OperationInvocation) { v.Receipt.Target.QualifiedIdentityDigest = "wrong" }},
					{"missing-stack-with-target", func(v *OperationInvocation) { v.Receipt.Stack = "" }},
					{"unknown-engine", func(v *OperationInvocation) { v.Receipt.Target.Engine = "wrong" }},
				}
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) { v := base(code, action); tc.mutate(&v); check(t, v, false) })
				}
				v := base(code, action)
				v.Receipt.Outcome = "unknown"
				v.Receipt.Effects = "unknown"
				v.Status = "unknown"
				v.Receipt.Errors = append(v.Receipt.Errors, closeDiag, commitDiag, releaseDiag)
				check(t, v, true)
				// A syntactically valid target is a claim, not independently verified actual identity.
				// Nil-target phase semantics are intentionally not tightened pending provider clarification.
			})
		}
	}
}

func TestBoundReceiptTrustedTargetComparison(t *testing.T) {
	q := &QualifiedOperationConfig{DaemonID: "independent-id"}
	endpoint := "unix:///synthetic.sock"
	r := &OperationReceipt{Operation: "up", Stack: "synthetic", Target: &OperationTarget{Engine: "docker", EndpointDigest: operationDigest(endpoint), QualifiedIdentityDigest: operationDigest("dill-local-docker-v1\x00" + q.DaemonID)}}
	if err := bindOperationReceipt(r, "up", "synthetic", "docker", endpoint, q); err != nil {
		t.Fatal(err)
	}
	r.Target.EndpointDigest = "sha256:" + strings.Repeat("3", 64)
	if err := bindOperationReceipt(r, "up", "synthetic", "docker", endpoint, q); err == nil {
		t.Fatal("accepted wrong endpoint")
	}
	r.Target.EndpointDigest = operationDigest(endpoint)
	r.Target.QualifiedIdentityDigest = "sha256:" + strings.Repeat("4", 64)
	if err := bindOperationReceipt(r, "up", "synthetic", "docker", endpoint, q); err == nil {
		t.Fatal("accepted wrong qualified identity")
	}
}
