package targetregistry

import (
	"errors"
	"sync"
	"testing"
)

func TestExactBindingAndApproval(t *testing.T) {
	r := registry(t)
	v := record()
	if err := r.Add(v, v); err != nil {
		t.Fatal(err)
	}
	changes := []func(*Record){
		func(x *Record) { x.ID = "other" }, func(x *Record) { x.Host = "other.example" }, func(x *Record) { x.Port++ }, func(x *Record) { x.User = "other" }, func(x *Record) { x.KeyPath = "/other" }, func(x *Record) { x.HostPublicKey = "other" }, func(x *Record) { x.HostKeySHA256 = "other" }, func(x *Record) { x.RemoteSocket = "/other" }, func(x *Record) { x.AgentPublicKey = "other" }, func(x *Record) { x.AgentFingerprint = "other" }, func(x *Record) { x.AgentInstallationID = "other" }, func(x *Record) { x.EngineID = "22222222-2222-4222-8222-222222222222" }, func(x *Record) { x.EngineKind = "podman" }, func(x *Record) { x.EngineSocket = "unix:///other" }, func(x *Record) { x.DaemonID = "other" }, func(x *Record) { x.Generation++ }, func(x *Record) { x.RevealKeyFingerprint = "other" }, func(x *Record) { x.Revision++ },
	}
	for i, change := range changes {
		x := v
		change(&x)
		if !errors.Is(v.Verify(x), ErrMismatch) {
			t.Fatalf("change %d accepted", i)
		}
	}
	next := v
	next.Host = "next.example"
	next.Revision++
	if !errors.Is(r.Update(v, next, ReviewedUpdate{}), ErrUnconfirmed) {
		t.Fatal("unreviewed update")
	}
	if err := r.Update(v, next, ReviewedUpdate{v, next}); err != nil {
		t.Fatal(err)
	}
	back := v
	back.Revision = 3
	if err := r.Update(next, back, ReviewedUpdate{next, back}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.Update(v, next, ReviewedUpdate{v, next}), ErrConflict) {
		t.Fatal("ABA accepted")
	}
	reopened, err := Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(v.ID)
	if err != nil || got != back {
		t.Fatal(got, err)
	}
	unknown := v
	unknown.ID = "unknown"
	proposed := unknown
	proposed.Revision++
	if !errors.Is(r.Update(unknown, proposed, ReviewedUpdate{unknown, proposed}), ErrNotFound) {
		t.Fatal("unknown update")
	}
}
func TestConcurrentUpdates(t *testing.T) {
	r := registry(t)
	old := record()
	if err := r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, host := range []string{"one.example", "two.example"} {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			next := old
			next.Revision++
			next.Host = host
			results <- r.Update(old, next, ReviewedUpdate{old, next})
		}(host)
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
}
func TestIncompleteBinding(t *testing.T) {
	for i, change := range []func(*Record){func(x *Record) { x.RemoteSocket = "" }, func(x *Record) { x.AgentPublicKey = "" }, func(x *Record) { x.AgentFingerprint = "" }, func(x *Record) { x.AgentInstallationID = "" }, func(x *Record) { x.EngineID = "" }, func(x *Record) { x.EngineKind = "" }, func(x *Record) { x.EngineSocket = "" }, func(x *Record) { x.DaemonID = "" }, func(x *Record) { x.Generation = 0 }, func(x *Record) { x.RevealKeyFingerprint = "" }} {
		x := record()
		change(&x)
		if x.Validate() == nil {
			t.Fatalf("accepted missing field %d", i)
		}
	}
}
