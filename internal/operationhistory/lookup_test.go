package operationhistory

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLookupTypedVersionDispatchAndCloning(t *testing.T) {
	s, _ := testStore()
	old, e := s.Begin(testIntent())
	if e != nil {
		t.Fatal(e)
	}
	p, _ := completePreview(t, s, intentV2())
	one, e := s.Lookup(old.ID())
	if e != nil || one.Version != Version || one.V1 == nil || one.V2 != nil {
		t.Fatal("v1", e)
	}
	direct, e := s.Get(old.ID())
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(one.V1)
	b, _ := json.Marshal(direct)
	if string(a) != string(b) {
		t.Fatal("v1 wire changed")
	}
	two, e := s.Lookup(p.ID())
	if e != nil || two.Version != VersionV2 || two.V2 == nil || two.V1 != nil {
		t.Fatal("v2", e)
	}
	two.V2.Preview.Result.Resources[0].Name = "changed"
	again, e := s.Lookup(p.ID())
	if e != nil || again.V2.Preview.Result.Resources[0].Name != "demo" {
		t.Fatal("alias", e)
	}
	for _, id := range []string{"../x", "br_" + strings.Repeat("a", 32)} {
		if _, e := s.Lookup(id); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("not found", e)
		}
	}
}
func TestLookupRejectsUnknownDuplicateAndNoncanonicalVersions(t *testing.T) {
	s, d := testStore()
	r, e := s.Begin(testIntent())
	if e != nil {
		t.Fatal(e)
	}
	name := r.ID() + ".json"
	good := string(d.files[name])
	p, _ := completePreview(t, s, intentV2())
	v2 := string(d.files[p.ID()+".json"])
	cases := map[string]string{
		"unknown":        strings.Replace(good, `"brine.operation/1"`, `"brine.operation/3"`, 1),
		"duplicate":      strings.Replace(good, `"version":`, `"version":"brine.operation/2","version":`, 1),
		"missing":        strings.Replace(good, `"version":"brine.operation/1",`, "", 1),
		"wrong-v1-to-v2": strings.Replace(good, `"brine.operation/1"`, `"brine.operation/2"`, 1),
		"wrong-v2-to-v1": strings.Replace(v2, `"brine.operation/2"`, `"brine.operation/1"`, 1),
		"unknown-field":  strings.Replace(good, `"version":`, `"source":"private","version":`, 1),
		"trailing":       good + " ",
		"invalid":        "{",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			d.files[r.ID()+".json"] = []byte(bad)
			if _, e := s.Lookup(r.ID()); !errors.Is(e, ErrUnsafe) {
				t.Fatal("corrupt lookup", e)
			}
		})
	}
}
func TestLookupReturnsClonedKnownFailureOverlays(t *testing.T) {
	s, d := testStore()
	old, e := s.Begin(testIntent())
	if e != nil || old.Started() != nil {
		t.Fatal(e)
	}
	d.fail = true
	if old.Terminal(terminal(true)) == nil {
		t.Fatal("v1 failure hidden")
	}
	v, e := s.Lookup(old.ID())
	if e != nil || v.V1 == nil || v.V1.Persistence != "failed_inspect" || v.V1.Invocation == nil {
		t.Fatal("v1 overlay", e)
	}
	v.V1.Invocation.Receipt.Outcome = "unknown"
	v, e = s.Lookup(old.ID())
	if e != nil || v.V1.Invocation.Receipt.Outcome != "succeeded" {
		t.Fatal("v1 overlay alias", e)
	}
	s, d = testStore()
	i := intentV2()
	p, e := s.BeginPreviewV2(i)
	if e != nil || p.Started() != nil {
		t.Fatal(e)
	}
	d.fail = true
	if p.TerminalPreview(previewV2(i)) == nil {
		t.Fatal("v2 failure hidden")
	}
	w, e := s.Lookup(p.ID())
	if e != nil || w.V2 == nil || w.V2.Persistence != "failed_inspect" || w.V2.Preview == nil {
		t.Fatal("v2 overlay", e)
	}
	w.V2.Preview.Result.Resources[0].Name = "changed"
	w, e = s.Lookup(p.ID())
	if e != nil || w.V2.Preview.Result.Resources[0].Name != "demo" {
		t.Fatal("v2 overlay alias", e)
	}
}
