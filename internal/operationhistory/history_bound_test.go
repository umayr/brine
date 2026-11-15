package operationhistory

import (
	"encoding/json"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

// These are literal producer contract cases, independent of the decoder's catalog.
func TestBoundApplyTerminalAndRecoveryMatrix(t *testing.T) {
	codes := []dill.OperationDiagnostic{{Code: "PREFLIGHT_MISMATCH", Message: "Reviewed resource selection changed. Review again before retrying."}, {Code: "PREFLIGHT_UNAVAILABLE", Message: "Complete preflight evidence unavailable."}}
	commit := dill.OperationDiagnostic{Code: "COORDINATION_COMMIT_FAILED", Message: "Durable outcome unavailable. Inspect coordination state before retrying."}
	release := dill.OperationDiagnostic{Code: "COORDINATION_RELEASE_FAILED", Message: "Engine outcome was recorded; local coordination cleanup failed."}
	closeDiag := dill.OperationDiagnostic{Code: "ENGINE_CLOSE_FAILED", Message: "Engine connection release failed. Inspect the target before retrying."}
	for _, code := range codes {
		for _, tc := range []struct {
			name                     string
			cleanup                  []dill.OperationDiagnostic
			outcome, effects, status string
		}{
			{"primary", nil, "failed", "none", "failed"},
			{"release", []dill.OperationDiagnostic{release}, "failed", "none", "failed"},
			{"commit", []dill.OperationDiagnostic{commit}, "unknown", "unknown", "unknown"},
			{"commit-release", []dill.OperationDiagnostic{commit, release}, "unknown", "unknown", "unknown"},
		} {
			t.Run(code.Code+"/"+tc.name, func(t *testing.T) {
				s, d := testStore()
				i := intentV2()
				p, review := completePreview(t, s, i)
				a, e := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
				if e != nil || a.Started() != nil {
					t.Fatal(e)
				}
				base := terminal(false)
				base.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
				base.Receipt.Target.QualifiedIdentityDigest = i.ExpectedTarget.QualifiedIdentityDigest
				base.ExitCode = 1
				base.Status = tc.status
				base.Receipt.Outcome = tc.outcome
				base.Receipt.Effects = tc.effects
				base.Receipt.Errors = append([]dill.OperationDiagnostic{code}, tc.cleanup...)
				// Contradictory axes, wrong target, and close appended to N must all fail
				// before the valid terminal is durably published.
				invalid := []dill.OperationInvocation{}
				clone := func(v dill.OperationInvocation) dill.OperationInvocation {
					b, _ := json.Marshal(v)
					var x dill.OperationInvocation
					_ = json.Unmarshal(b, &x)
					return x
				}
				wrong := clone(base)
				wrong.Receipt.Outcome = "succeeded"
				invalid = append(invalid, wrong)
				wrong = clone(base)
				wrong.Receipt.Target.QualifiedIdentityDigest = Digest("wrong")
				invalid = append(invalid, wrong)
				wrong = clone(base)
				wrong.Receipt.Errors = append(wrong.Receipt.Errors, closeDiag)
				invalid = append(invalid, wrong)
				wrong = clone(base)
				wrong.Receipt.Errors[0].Message = "untrusted raw diagnostic"
				invalid = append(invalid, wrong)
				wrong = clone(base)
				if tc.name == "release" {
					wrong.Receipt.Outcome = "unknown"
					wrong.Receipt.Effects = "unknown"
					wrong.Status = "unknown"
				} else {
					wrong.Receipt.Effects = "partial"
				}
				invalid = append(invalid, wrong)
				for _, bad := range invalid {
					if a.TerminalApply(bad) == nil {
						t.Fatal("accepted contradiction")
					}
				}
				if e = a.TerminalApply(base); e != nil {
					t.Fatal("valid terminal", e)
				}
				got, e := s.GetV2(a.ID())
				if e != nil || got.Apply.Status != tc.status || got.Apply.Receipt.Outcome != tc.outcome || got.Apply.Receipt.Effects != tc.effects || len(got.Apply.Receipt.Errors) != 1+len(tc.cleanup) {
					t.Fatal("axes", e)
				}
				b, _ := json.Marshal(base)
				if _, e = dill.DecodeOperationInvocation(b); e == nil {
					t.Fatal("legacy parser widened")
				}
				legacyStore, _ := testStore()
				legacy, e := legacyStore.Begin(testIntent())
				if e != nil || legacy.Started() != nil {
					t.Fatal("legacy setup", e)
				}
				if legacy.Terminal(base) == nil {
					t.Fatal("v1 accepted bound-only diagnostic")
				}
				reopened, e := openStore(d)
				if e != nil {
					t.Fatal("restart", e)
				}
				got, e = reopened.GetV2(a.ID())
				if e != nil || got.Phase != "terminal" || got.Apply.Receipt.Outcome != tc.outcome || got.Apply.Receipt.Effects != tc.effects {
					t.Fatal("restart axes", e)
				}
				if _, e = reopened.BeginApplyV2(i, p.ID(), review.ReviewDigest); e == nil {
					t.Fatal("review reused")
				}
			})
		}
	}
}

func TestBoundApplyOldOnlyCloseRetainsOldSemantics(t *testing.T) {
	s, _ := testStore()
	i := intentV2()
	p, review := completePreview(t, s, i)
	a, e := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
	if e != nil || a.Started() != nil {
		t.Fatal(e)
	}
	v := terminal(false)
	v.ExitCode = 1
	v.Status = "unknown"
	v.Receipt.Outcome = "unknown"
	v.Receipt.Effects = "unknown"
	v.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
	v.Receipt.Target.QualifiedIdentityDigest = i.ExpectedTarget.QualifiedIdentityDigest
	v.Receipt.Errors = []dill.OperationDiagnostic{{Code: "ENGINE_CLOSE_FAILED", Message: "Engine connection release failed. Inspect the target before retrying."}}
	if e := a.TerminalApply(v); e != nil {
		t.Fatal(e)
	}
	r, e := s.GetV2(a.ID())
	if e != nil || r.Apply.Status != "unknown" || r.Apply.Receipt.Outcome != "unknown" {
		t.Fatal("old semantics lost", e)
	}
}
