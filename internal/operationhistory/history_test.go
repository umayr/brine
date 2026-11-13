package operationhistory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

type memoryDisk struct {
	files map[string][]byte
	fail  bool
}

func (d *memoryDisk) read(n string) ([]byte, error) {
	b, ok := d.files[n]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}
func (d *memoryDisk) write(n string, b []byte) error {
	if d.fail {
		return ErrUnsafe
	}
	d.files[n] = append([]byte(nil), b...)
	return nil
}
func (d *memoryDisk) names() ([]string, error) {
	var n []string
	for k := range d.files {
		n = append(n, k)
	}
	return n, nil
}
func (d *memoryDisk) close() error { return nil }
func testIntent() Intent {
	return Intent{Action: "stop", Stack: "demo", Actor: Digest("trusted-user"), Entrypoint: "compose.pkl", SelectorDigest: Digest("/synthetic/socket"), ExpectedIdentityDigest: Digest("qualified-identity")}
}
func testStore() (*Store, *memoryDisk) {
	d := &memoryDisk{files: map[string][]byte{}}
	return &Store{disk: d, known: map[string]Record{}}, d
}
func terminal(cleanup bool) dill.OperationInvocation {
	i := testIntent()
	receipt := &dill.OperationReceipt{Version: dill.OperationVersion, OperationID: "op_" + strings.Repeat("a", 32), Operation: i.Action, Stack: i.Stack, Target: &dill.OperationTarget{Engine: "docker", EndpointDigest: i.SelectorDigest, QualifiedIdentityDigest: i.ExpectedIdentityDigest}, Outcome: "succeeded", Effects: "complete", Resources: []dill.OperationResource{}, Errors: []dill.OperationDiagnostic{}}
	v := dill.OperationInvocation{Receipt: receipt, ExitCode: 0, Status: "succeeded"}
	if cleanup {
		v.ExitCode = 1
		v.Status = "failed"
		receipt.Errors = []dill.OperationDiagnostic{{Code: "COORDINATION_RELEASE_FAILED", Message: "Engine outcome was recorded; local coordination cleanup failed."}}
	}
	return v
}
func TestAcceptedStartedTerminalDeliveryIndependentAxes(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		s, d := testStore()
		request, e := s.Begin(testIntent())
		if e != nil {
			t.Fatal(e)
		}
		accepted, e := s.Get(request.ID())
		if e != nil || accepted.Phase != "accepted" || accepted.Invocation != nil || accepted.DillOperationID != nil || accepted.StartedAt != nil || accepted.SourceBinding != "UNBOUND" {
			t.Fatalf("invalid accepted: %+v %v", accepted, e)
		}
		if request.Started() != nil {
			t.Fatal("start failed")
		}
		expected := terminal(cleanup)
		if request.Terminal(expected) != nil {
			t.Fatal("terminal failed")
		}
		if request.Delivery(true) != nil {
			t.Fatal("delivery failed")
		}
		got, e := s.Get(request.ID())
		if e != nil || got.Status != expected.Status || got.Invocation.Receipt.Outcome != "succeeded" || got.Invocation.Receipt.Effects != "complete" || got.Invocation.ExitCode != expected.ExitCode || got.Delivery != "write_failed" {
			t.Fatalf("axes: %+v %v", got, e)
		}
		got.Invocation.Receipt.Outcome = "unknown"
		again, _ := s.Get(request.ID())
		if again.Invocation.Receipt.Outcome != "succeeded" {
			t.Fatal("caller mutated store")
		}
		for _, b := range d.files {
			if strings.Contains(string(b), "synthetic/socket") || strings.Contains(string(b), "trusted-user") {
				t.Fatal("raw path/identity persisted")
			}
		}
	}
}
func TestPersistenceFaultsPreserveKnownReceiptAndBlockNewRequest(t *testing.T) {
	s, d := testStore()
	first, e := s.Begin(testIntent())
	if e != nil || first.Started() != nil || first.Terminal(terminal(false)) != nil {
		t.Fatal("positive control")
	}
	request, e := s.Begin(testIntent())
	if e != nil || request.Started() != nil {
		t.Fatal("positive setup")
	}
	d.fail = true
	if request.Terminal(terminal(true)) == nil {
		t.Fatal("fault hidden")
	}
	got, e := s.Get(request.ID())
	if e != nil || got.Persistence != "failed_inspect" || got.Reason != "history_failure_requires_inspection" || got.Invocation == nil || got.Invocation.Receipt.Outcome != "succeeded" || got.Invocation.Receipt.Effects != "complete" || got.Invocation.ExitCode != 1 || got.Status != "failed" {
		t.Fatalf("known outcome lost: %+v %v", got, e)
	}
	if _, e = s.Begin(testIntent()); e == nil {
		t.Fatal("launched through poisoned store")
	}
	durable, e := decode(d.files[request.ID()+".json"])
	if e != nil || durable.Phase != "running" || durable.DillOperationID != nil {
		t.Fatalf("invented durable completion: %+v %v", durable, e)
	}
}
func TestIntentFaultHasNoAcceptedRequestAndStartedFaultFails(t *testing.T) {
	s, d := testStore()
	d.fail = true
	if r, e := s.Begin(testIntent()); e == nil || r != nil || len(d.files) != 0 {
		t.Fatal("accepted failed intent")
	}
	s, d = testStore()
	r, e := s.Begin(testIntent())
	if e != nil {
		t.Fatal(e)
	}
	d.fail = true
	if r.Started() == nil {
		t.Fatal("start fault hidden")
	}
}
func TestClosedRecordCorruptionAfterPositive(t *testing.T) {
	s, d := testStore()
	r, e := s.Begin(testIntent())
	if e != nil {
		t.Fatal(e)
	}
	good := d.files[r.ID()+".json"]
	if _, e = decode(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{append(good, []byte(" ")...), []byte(strings.Replace(string(good), `"version":`, `"rawSource":"PRIVATE_SOURCE","version":`, 1)), []byte(strings.Replace(string(good), `"version":`, `"version":"brine.operation/1","version":`, 1)), []byte(strings.Replace(string(good), `"sourceBinding":"UNBOUND"`, `"sourceBinding":"approved"`, 1)), []byte(`{}`)} {
		if _, e = decode(bad); e == nil {
			t.Fatal("accepted corrupt record")
		}
	}
	d.files[r.ID()+".json"] = []byte(`PRIVATE_STDERR`)
	if _, e = s.Get(r.ID()); !errors.Is(e, ErrUnsafe) {
		t.Fatal("repaired corruption")
	}
}
func TestMalformedOrMissingTerminalHasNullableDillID(t *testing.T) {
	s, _ := testStore()
	r, e := s.Begin(testIntent())
	if e != nil || r.Started() != nil {
		t.Fatal("setup")
	}
	if r.Terminal(dill.OperationInvocation{ExitCode: 0, Status: "succeeded"}) == nil {
		t.Fatal("accepted missing receipt success")
	}
	if r.Terminal(dill.OperationInvocation{ExitCode: 0, Status: "unknown"}) != nil {
		t.Fatal("missing receipt unknown rejected")
	}
	got, _ := s.Get(r.ID())
	if got.DillOperationID != nil || got.Invocation.Receipt != nil || got.Status != "unknown" {
		t.Fatal("invented identity")
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "PRIVATE_") {
		t.Fatal("diagnostic leaked")
	}
}
func TestStrictIDAndRootSeparation(t *testing.T) {
	for _, id := range []string{"", "../record", "br_" + strings.Repeat("A", 32), "op_" + strings.Repeat("a", 32), "br_" + strings.Repeat("a", 32) + "/x"} {
		if ValidID(id) {
			t.Fatal(id)
		}
	}
	base, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	for _, relative := range [][3]string{{"data/history", "data", "coord"}, {".", "data", "coord"}, {"history", "data", "history/coord"}} {
		paths := [3]string{filepath.Join(base, relative[0]), filepath.Join(base, relative[1]), filepath.Join(base, relative[2])}
		if SeparateRoots(paths[0], paths[1], paths[2]) == nil {
			t.Fatal("nested roots accepted", paths)
		}
	}
	if SeparateRoots(filepath.Join(base, "history"), filepath.Join(base, "data"), filepath.Join(base, "coord")) != nil {
		t.Fatal("separate canonical prospective siblings rejected")
	}
}
