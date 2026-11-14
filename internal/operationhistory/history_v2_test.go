package operationhistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

func intentV2() BoundIntent {
	return BoundIntent{Action: "stop", Stack: "demo", Actor: Digest("actor"), Source: SourceRef{Digest("snapshot"), Digest("profile"), Digest("evaluator")}, ExpectedTarget: dill.PreflightTarget{Engine: "docker", EndpointDigest: Digest("selector"), QualifiedIdentityDigest: Digest("target")}}
}
func previewV2(i BoundIntent) dill.PreflightInvocation {
	input, selection := Digest("input"), Digest("selection")
	exit := 0
	return dill.PreflightInvocation{Status: "succeeded", ProcessExit: &exit, Result: &dill.PreflightResult{Version: dill.PreflightVersion, OperationID: "op_" + strings.Repeat("a", 32), Operation: i.Action, Target: &i.ExpectedTarget, Stack: i.Stack, Outcome: "succeeded", EngineEffects: "none", CoordinationEffects: "written", InputDigest: &input, SelectionDigest: &selection, Coverage: dill.PreflightCoverage{Containers: dill.PreflightScope{Scope: "declared-stack", Complete: true}, Network: dill.PreflightScope{Scope: "stack-network", Count: 1, Complete: true}, Volumes: dill.PreflightScope{Scope: "declared-attached-named", Complete: true}}, Resources: []dill.PreflightResource{{Kind: "network", Name: "demo", Identity: "demo", IdentityKind: "planned-name", Presence: "absent", Action: "retain", Retention: "preserve"}}, Errors: []dill.OperationDiagnostic{}}}
}
func completePreview(t *testing.T, s *Store, i BoundIntent) (*RequestV2, RecordV2) {
	t.Helper()
	p, e := s.BeginPreviewV2(i)
	if e != nil || p.Started() != nil || p.TerminalPreview(previewV2(i)) != nil {
		t.Fatalf("preview: %v", e)
	}
	r, e := s.GetV2(p.ID())
	if e != nil {
		t.Fatal(e)
	}
	return p, r
}
func TestV2ReviewLinkAndClosedEncoding(t *testing.T) {
	s, d := testStore()
	i := intentV2()
	p, pr := completePreview(t, s, i)
	if pr.ReviewDigest == "" || pr.Preview.Result.OperationID == "" {
		t.Fatal("missing review")
	}
	for name, mutate := range map[string]func(*BoundIntent){
		"actor":     func(x *BoundIntent) { x.Actor = Digest("other") },
		"action":    func(x *BoundIntent) { x.Action = "up" },
		"stack":     func(x *BoundIntent) { x.Stack = "other" },
		"snapshot":  func(x *BoundIntent) { x.Source.SnapshotDigest = Digest("other") },
		"profile":   func(x *BoundIntent) { x.Source.EvaluatorProfileDigest = Digest("other") },
		"evaluator": func(x *BoundIntent) { x.Source.EvaluatorRefDigest = Digest("other") },
		"selector":  func(x *BoundIntent) { x.ExpectedTarget.EndpointDigest = Digest("other") },
		"target":    func(x *BoundIntent) { x.ExpectedTarget.QualifiedIdentityDigest = Digest("other") },
	} {
		bad := i
		mutate(&bad)
		if _, e := s.BeginApplyV2(bad, p.ID(), pr.ReviewDigest); e == nil {
			t.Fatal("context mismatch accepted", name)
		}
	}
	if _, e := s.BeginApplyV2(i, p.ID(), Digest("wrong")); e == nil {
		t.Fatal("wrong digest accepted")
	}
	a, e := s.BeginApplyV2(i, p.ID(), pr.ReviewDigest)
	if e != nil || a.ID() == p.ID() {
		t.Fatal("apply", e)
	}
	b := d.files[p.ID()+".json"]
	if _, e := decodeV2(b); e != nil {
		t.Fatal(e)
	}
	for _, x := range [][]byte{append(append([]byte{}, b...), ' '), []byte(strings.Replace(string(b), `"version":`, `"rawSource":"secret","version":`, 1)), []byte(strings.Replace(string(b), `"brine.operation/2"`, `"brine.operation/1"`, 1)), append(append([]byte{}, b...), make([]byte, MaxRecordV2)...)} {
		if _, e := decodeV2(x); e == nil {
			t.Fatal("accepted noncanonical or oversized")
		}
	}
	if _, e := s.Get(p.ID()); !errors.Is(e, ErrUnsafe) {
		t.Fatal("v2 decoded as v1", e)
	}
}
func TestMixedV1V2StoreRemainsVersionSeparated(t *testing.T) {
	s, _ := testStore()
	legacy, err := s.Begin(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := completePreview(t, s, intentV2())
	if _, err := s.Begin(testIntent()); err != nil {
		t.Fatal("v1 rejected valid v2 neighbor", err)
	}
	if _, err := s.BeginPreviewV2(intentV2()); err != nil {
		t.Fatal("v2 rejected valid v1 neighbor", err)
	}
	if _, err := s.GetV2(legacy.ID()); !errors.Is(err, ErrUnsafe) {
		t.Fatal("v1 treated as v2", err)
	}
	if _, err := s.Get(preview.ID()); !errors.Is(err, ErrUnsafe) {
		t.Fatal("v2 treated as v1", err)
	}
	old, err := s.Get(legacy.ID())
	if err != nil || old.SourceBinding != "UNBOUND" {
		t.Fatal("v1 changed", err)
	}
}

func TestV2MemoryRestartRecoversOnlyPendingRecords(t *testing.T) {
	s, d := testStore()
	i := intentV2()
	preview, review := completePreview(t, s, i)
	accepted, err := s.BeginPreviewV2(i)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := s.BeginApplyV2(i, preview.ID(), review.ReviewDigest)
	if err != nil || apply.Started() != nil {
		t.Fatal(err)
	}
	legacy, err := s.Begin(testIntent())
	if err != nil {
		t.Fatal(err)
	}
	namesBefore := len(d.files)
	reopened, err := openStore(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{accepted.ID(), apply.ID()} {
		r, err := reopened.GetV2(id)
		if err != nil || r.Phase != "unknown" || r.RecoveryObservedAt == nil || r.EndObservedAt != nil || r.Preview != nil || r.Apply != nil {
			t.Fatalf("recovery %s: %+v %v", id, r, err)
		}
	}
	old, err := reopened.Get(legacy.ID())
	if err != nil || old.Phase != "unknown" || old.EndObservedAt != nil {
		t.Fatal("legacy recovery", err)
	}
	got, err := reopened.GetV2(preview.ID())
	if err != nil || got.Phase != "terminal" || got.ReviewDigest != review.ReviewDigest || len(d.files) != namesBefore {
		t.Fatal("review changed or replayed", err)
	}
}

func TestV2RestartValidatesAllBeforeAnyRecovery(t *testing.T) {
	for _, fault := range []string{"corrupt", "missing-review", "write"} {
		t.Run(fault, func(t *testing.T) {
			s, d := testStore()
			i := intentV2()
			preview, review := completePreview(t, s, i)
			pending, err := s.BeginApplyV2(i, preview.ID(), review.ReviewDigest)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), d.files[pending.ID()+".json"]...)
			switch fault {
			case "corrupt":
				d.files[preview.ID()+".json"] = []byte(`{"rawSource":"private"}`)
			case "missing-review":
				delete(d.files, preview.ID()+".json")
			case "write":
				d.fail = true
			}
			if reopened, err := openStore(d); err == nil {
				reopened.Close()
				t.Fatal("unsafe restart accepted")
			}
			if string(d.files[pending.ID()+".json"]) != string(before) {
				t.Fatal("pending record changed despite failed recovery")
			}
		})
	}
}

func TestV2IntentAndStartFaultNeverPublishTerminal(t *testing.T) {
	s, d := testStore()
	d.fail = true
	if p, err := s.BeginPreviewV2(intentV2()); err == nil || p != nil || len(d.files) != 0 {
		t.Fatal("accepted failed intent")
	}
	s, d = testStore()
	p, err := s.BeginPreviewV2(intentV2())
	if err != nil {
		t.Fatal(err)
	}
	d.fail = true
	if p.Started() == nil {
		t.Fatal("start write failure hidden")
	}
	if err := p.TerminalPreview(previewV2(intentV2())); err == nil {
		t.Fatal("terminal published after failure")
	}
	r, err := decodeV2(d.files[p.ID()+".json"])
	if err != nil || r.Phase != "accepted" || r.Preview != nil {
		t.Fatal("invented durable result", err)
	}
}

func TestV2ApplyPreservesIndependentInvocationAxes(t *testing.T) {
	s, _ := testStore()
	i := intentV2()
	p, preview := completePreview(t, s, i)
	a, err := s.BeginApplyV2(i, p.ID(), preview.ReviewDigest)
	if err != nil || a.Started() != nil {
		t.Fatal(err)
	}
	inv := terminal(true)
	inv.Receipt.OperationID = "op_" + strings.Repeat("b", 32)
	inv.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
	inv.Receipt.Target.QualifiedIdentityDigest = i.ExpectedTarget.QualifiedIdentityDigest
	if err := a.TerminalApply(inv); err != nil {
		t.Fatal(err)
	}
	if err := a.Delivery(true); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetV2(a.ID())
	if err != nil || got.Apply.Status != "failed" || got.Apply.ExitCode != 1 || got.Apply.Receipt.Outcome != "succeeded" || got.Apply.Receipt.Effects != "complete" || got.Delivery != "write_failed" || got.Apply.Receipt.OperationID == preview.Preview.Result.OperationID {
		t.Fatalf("lost independent axes: %+v %v", got, err)
	}
}

// A reported failure after publication models an uncertain fsync outcome: the
// caller must suppress its marker even if a valid terminal later survives.
type ambiguousDisk struct {
	*memoryDisk
	failAfterWrite bool
}

func (d *ambiguousDisk) write(name string, b []byte) error {
	if err := d.memoryDisk.write(name, b); err != nil {
		return err
	}
	if d.failAfterWrite {
		return ErrUnsafe
	}
	return nil
}
func TestV2AmbiguousPublicationNeverClaimsDurabilityInProcess(t *testing.T) {
	base := &memoryDisk{files: map[string][]byte{}}
	d := &ambiguousDisk{memoryDisk: base}
	s := &Store{disk: d, known: map[string]Record{}}
	i := intentV2()
	p, err := s.BeginPreviewV2(i)
	if err != nil || p.Started() != nil {
		t.Fatal(err)
	}
	d.failAfterWrite = true
	if p.TerminalPreview(previewV2(i)) == nil {
		t.Fatal("ambiguous publication reported success")
	}
	known, err := s.GetV2(p.ID())
	if err != nil || known.Persistence != "failed_inspect" || known.Preview == nil {
		t.Fatal("lost known axes", err)
	}
	if _, err := s.BeginPreviewV2(i); err == nil {
		t.Fatal("poisoned store accepted intent")
	}
	d.failAfterWrite = false
	restarted, err := openStore(d)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := restarted.GetV2(p.ID())
	if err != nil || durable.Phase != "terminal" || durable.Persistence != "durable" || durable.ReviewDigest == "" {
		t.Fatal("valid published terminal lost on restart", err)
	}
}

func TestV2PersistenceFailureRetainsNondurableKnownPreview(t *testing.T) {
	s, d := testStore()
	i := intentV2()
	p, e := s.BeginPreviewV2(i)
	if e != nil || p.Started() != nil {
		t.Fatal(e)
	}
	d.fail = true
	if p.TerminalPreview(previewV2(i)) == nil {
		t.Fatal("failure hidden")
	}
	r, e := s.GetV2(p.ID())
	if e != nil || r.Persistence != "failed_inspect" || r.Preview == nil {
		t.Fatal("lost known result", e)
	}
	durable, e := decodeV2(d.files[p.ID()+".json"])
	if e != nil || durable.Phase != "running" {
		t.Fatal("invented terminal", e)
	}
	if _, e = s.BeginApplyV2(i, p.ID(), r.ReviewDigest); e == nil {
		t.Fatal("failed store admitted apply")
	}
}
func TestV2Full256RowReviewAndBudget(t *testing.T) {
	s, _ := testStore()
	i := intentV2()
	p, err := s.BeginPreviewV2(i)
	if err != nil || p.Started() != nil {
		t.Fatal(err)
	}
	inv := previewV2(i)
	for n := 0; n < 255; n++ {
		name := fmt.Sprintf("c%03d%s", n, strings.Repeat("x", 251))
		inv.Result.Resources = append(inv.Result.Resources, dill.PreflightResource{Kind: "container", Name: name, Identity: name, IdentityKind: "planned-name", Presence: "absent", Action: "none", Retention: "not-applicable"})
	}
	inv.Result.Coverage.Containers.Count = 255
	if err := p.TerminalPreview(inv); err != nil {
		t.Fatal("full review rejected", err)
	}
	r, err := s.GetV2(p.ID())
	if err != nil || len(r.Preview.Result.Resources) != 256 {
		t.Fatal("rows lost", err)
	}
	inv.Result.Resources = append(inv.Result.Resources, inv.Result.Resources[0])
	if _, err := decodePreview(&inv); err == nil {
		t.Fatal("257 rows accepted")
	}
}

func TestV2PreviewResultMutationCannotChangeStoredReview(t *testing.T) {
	s, _ := testStore()
	i := intentV2()
	p, r := completePreview(t, s, i)
	r.Preview.Result.Resources[0].Name = "changed"
	got, e := s.GetV2(p.ID())
	if e != nil || got.Preview.Result.Resources[0].Name != "demo" {
		t.Fatal("alias", e)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "snapshot\"") {
		t.Fatal("raw source material")
	}
}
