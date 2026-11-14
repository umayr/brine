package operationhistory

import (
	"encoding/json"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

func failedPreview(i BoundIntent) dill.PreflightInvocation {
	v := previewV2(i)
	exit := 1
	v.ProcessExit = &exit
	v.Status = "failed"
	r := v.Result
	r.Outcome = "failed"
	r.InputDigest = nil
	r.SelectionDigest = nil
	r.Resources = []dill.PreflightResource{}
	r.Coverage.Containers.Complete = false
	r.Coverage.Network.Count = 0
	r.Coverage.Network.Complete = false
	r.Coverage.Volumes.Complete = false
	r.Errors = []dill.OperationDiagnostic{{Code: "PREFLIGHT_UNAVAILABLE", Message: "Complete preflight evidence unavailable."}}
	return v
}
func clonePreview(v dill.PreflightInvocation) dill.PreflightInvocation {
	b, _ := json.Marshal(v)
	var c dill.PreflightInvocation
	_ = json.Unmarshal(b, &c)
	return c
}
func TestPreviewFailureRejectsAllAvailableContextMismatch(t *testing.T) {
	i := intentV2()
	good := failedPreview(i)
	for name, mutate := range map[string]func(*dill.PreflightInvocation){
		"action":    func(v *dill.PreflightInvocation) { v.Result.Operation = "up" },
		"stack":     func(v *dill.PreflightInvocation) { v.Result.Stack = "other" },
		"endpoint":  func(v *dill.PreflightInvocation) { v.Result.Target.EndpointDigest = Digest("other") },
		"qualified": func(v *dill.PreflightInvocation) { v.Result.Target.QualifiedIdentityDigest = Digest("other") },
	} {
		t.Run(name, func(t *testing.T) {
			s, d := testStore()
			p, e := s.BeginPreviewV2(i)
			if e != nil || p.Started() != nil {
				t.Fatal(e)
			}
			bad := clonePreview(good)
			mutate(&bad)
			if p.TerminalPreview(bad) == nil {
				t.Fatal("mismatched failed preview accepted")
			}
			if e := p.TerminalPreview(good); e != nil {
				t.Fatal("matching failure rejected", e)
			}
			r, e := s.GetV2(p.ID())
			if e != nil || r.Preview.Status != "failed" || r.ReviewDigest != "" {
				t.Fatal("failure axes", e)
			}
			reopened, e := openStore(d)
			if e != nil {
				t.Fatal(e)
			}
			r, e = reopened.GetV2(p.ID())
			if e != nil || r.Preview.Result.Outcome != "failed" {
				t.Fatal("failure lost", e)
			}
			if _, e = reopened.BeginApplyV2(i, p.ID(), Digest("review")); e == nil {
				t.Fatal("failed review usable")
			}
			mutate(r.Preview)
			corrupt, _ := json.Marshal(r)
			d.files[p.ID()+".json"] = corrupt
			if unsafe, err := openStore(d); err == nil {
				unsafe.Close()
				t.Fatal("mismatched durable context accepted on restart")
			}
		})
	}
}
func TestPreviewEarlyFailureDoesNotInventContext(t *testing.T) {
	i := intentV2()
	s, _ := testStore()
	p, e := s.BeginPreviewV2(i)
	if e != nil || p.Started() != nil {
		t.Fatal(e)
	}
	v := failedPreview(i)
	v.Result.Target = nil
	v.Result.Stack = ""
	v.Result.CoordinationEffects = "none"
	v.Result.Errors = []dill.OperationDiagnostic{{Code: "EVALUATION_FAILED", Message: "Configuration evaluation failed."}}
	if e := p.TerminalPreview(v); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetV2(p.ID())
	if e != nil || got.Preview.Result.Target != nil || got.Preview.Result.Stack != "" {
		t.Fatal("invented context", e)
	}
}
func TestApplyEarlyInvalidInputRetainsNullTarget(t *testing.T) {
	i := intentV2()
	s, _ := testStore()
	p, review := completePreview(t, s, i)
	a, err := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
	if err != nil || a.Started() != nil {
		t.Fatal(err)
	}
	v := terminal(false)
	v.ExitCode = 2
	v.Status = "failed"
	v.Receipt.Target = nil
	v.Receipt.Stack = ""
	v.Receipt.Outcome = "invalid_input"
	v.Receipt.Effects = "none"
	v.Receipt.Errors = []dill.OperationDiagnostic{{Code: "INVALID_ARGUMENTS", Message: "Invalid operation arguments."}}
	if err := a.TerminalApply(v); err != nil {
		t.Fatal("valid early rejection", err)
	}
	got, err := s.GetV2(a.ID())
	if err != nil || got.Apply.Receipt.Target != nil || got.Apply.Receipt.Stack != "" {
		t.Fatal("invented target", err)
	}
}

func TestApplySuccessRequiresActualQualifiedIdentity(t *testing.T) {
	i := intentV2()
	s, d := testStore()
	p, review := completePreview(t, s, i)
	a, e := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
	if e != nil || a.Started() != nil {
		t.Fatal(e)
	}
	v := terminal(false)
	v.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
	v.Receipt.Target.QualifiedIdentityDigest = ""
	if a.TerminalApply(v) == nil {
		t.Fatal("success without actual identity accepted")
	}
	unknown := terminal(false)
	unknown.ExitCode = 1
	unknown.Status = "unknown"
	unknown.Receipt.Outcome = "unknown"
	unknown.Receipt.Effects = "unknown"
	unknown.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
	unknown.Receipt.Target.QualifiedIdentityDigest = ""
	unknown.Receipt.Errors = []dill.OperationDiagnostic{{Code: "ENGINE_CLOSE_FAILED", Message: "Engine connection release failed. Inspect the target before retrying."}}
	if a.TerminalApply(unknown) == nil {
		t.Fatal("unknown effects without qualified identity accepted")
	}
	v.Receipt.Target.QualifiedIdentityDigest = i.ExpectedTarget.QualifiedIdentityDigest
	if e := a.TerminalApply(v); e != nil {
		t.Fatal("matched qualified success", e)
	}
	reopened, e := openStore(d)
	if e != nil {
		t.Fatal(e)
	}
	got, e := reopened.GetV2(a.ID())
	if e != nil || got.Apply.Receipt.Target.QualifiedIdentityDigest != i.ExpectedTarget.QualifiedIdentityDigest {
		t.Fatal("identity lost", e)
	}
	got.Apply.Receipt.Target.QualifiedIdentityDigest = ""
	bad, _ := json.Marshal(got)
	d.files[a.ID()+".json"] = bad
	if reopened, err := openStore(d); err == nil {
		reopened.Close()
		t.Fatal("invalid durable terminal accepted on restart")
	}
}
