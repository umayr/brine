package operationhistory

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestInconsistentDurableApplyLinkFailsClosed(t *testing.T) {
	for _, which := range []string{"lookup", "get", "begin-preview", "begin-apply", "begin-v1", "restart"} {
		t.Run(which, func(t *testing.T) {
			s, d := testStore()
			i := intentV2()
			p, review := completePreview(t, s, i)
			a, e := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := s.Begin(testIntent()); e != nil {
				t.Fatal("mixed positive", e)
			}
			r, e := s.GetV2(a.ID())
			if e != nil {
				t.Fatal("positive link", e)
			}
			r.Intent.Source.SnapshotDigest = Digest("different")
			b, _ := json.Marshal(r)
			if _, e := decodeV2(b); e != nil {
				t.Fatal("local shape must remain valid", e)
			}
			d.files[a.ID()+".json"] = b
			switch which {
			case "lookup":
				_, e = s.Lookup(a.ID())
			case "get":
				_, e = s.GetV2(a.ID())
			case "begin-preview":
				_, e = s.BeginPreviewV2(i)
			case "begin-apply":
				_, e = s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
			case "begin-v1":
				_, e = s.Begin(testIntent())
			case "restart":
				_, e = openStore(d)
			}
			if !errors.Is(e, ErrUnsafe) {
				t.Fatalf("%s accepted inconsistent link: %v", which, e)
			}
		})
	}
}
func TestUpdateDoesNotRepairInconsistentDurableLink(t *testing.T) {
	s, d := testStore()
	i := intentV2()
	p, review := completePreview(t, s, i)
	a, err := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.GetV2(a.ID())
	if err != nil {
		t.Fatal(err)
	}
	r.Intent.Source.SnapshotDigest = Digest("different")
	corrupt, _ := json.Marshal(r)
	d.files[a.ID()+".json"] = corrupt
	if a.Started() == nil {
		t.Fatal("accepted update against inconsistent disk")
	}
	if string(d.files[a.ID()+".json"]) != string(corrupt) {
		t.Fatal("repaired inconsistent record")
	}
	got, err := s.Lookup(a.ID())
	if err != nil || got.V2 == nil || got.V2.Persistence != "failed_inspect" {
		t.Fatal("lost nondurable known observation", err)
	}
}

func TestInconsistentLinkDoesNotEraseKnownFailureOverlay(t *testing.T) {
	s, d := testStore()
	i := intentV2()
	p, review := completePreview(t, s, i)
	a, e := s.BeginApplyV2(i, p.ID(), review.ReviewDigest)
	if e != nil || a.Started() != nil {
		t.Fatal(e)
	}
	v := terminal(false)
	v.Receipt.Target.EndpointDigest = i.ExpectedTarget.EndpointDigest
	v.Receipt.Target.QualifiedIdentityDigest = i.ExpectedTarget.QualifiedIdentityDigest
	d.fail = true
	if a.TerminalApply(v) == nil {
		t.Fatal("fault hidden")
	}
	r, e := decodeV2(d.files[a.ID()+".json"])
	if e != nil {
		t.Fatal(e)
	}
	r.Intent.Source.SnapshotDigest = Digest("different")
	b, _ := json.Marshal(r)
	d.files[a.ID()+".json"] = b
	got, e := s.GetV2(a.ID())
	if e != nil || got.Persistence != "failed_inspect" || got.Apply == nil {
		t.Fatal("known GetV2 lost", e)
	}
	union, e := s.Lookup(a.ID())
	if e != nil || union.V2 == nil || union.V2.Persistence != "failed_inspect" || union.V2.Apply == nil {
		t.Fatal("known Lookup lost", e)
	}
	union.V2.Apply.Receipt.Outcome = "unknown"
	again, e := s.Lookup(a.ID())
	if e != nil || again.V2.Apply.Receipt.Outcome != "succeeded" {
		t.Fatal("overlay not cloned", e)
	}
}
