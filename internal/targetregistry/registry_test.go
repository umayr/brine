package targetregistry

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

var testKey, pin = func() (string, string) {
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	if err != nil {
		panic(err)
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n"), ssh.FingerprintSHA256(pub)
}()

func record() Record {
	return Record{ID: "server", Host: "host.example", Port: 22, User: "operator", KeyPath: "/home/operator/.ssh/id_ed25519", HostPublicKey: testKey, HostKeySHA256: pin, AgentInstallationID: "install-1", EngineID: "engine-1"}
}
func registry(t *testing.T) *Registry {
	t.Helper()
	r, e := Open(filepath.Join(t.TempDir(), "registry"))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestEnrollmentAndIdentity(t *testing.T) {
	r := registry(t)
	v := record()
	if !errors.Is(r.Add(v, false), ErrUnconfirmed) {
		t.Fatal("unconfirmed add")
	}
	if e := r.Add(v, true); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(r.Add(v, true), ErrConflict) {
		t.Fatal("duplicate accepted")
	}
	got, e := r.Get(v.ID)
	if e != nil || got != v {
		t.Fatal(got, e)
	}
	good := Identity{pin, v.AgentInstallationID, v.EngineID}
	if e = v.Verify(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Identity{{}, {"SHA256:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", good.AgentInstallationID, good.EngineID}, {pin, "other", good.EngineID}, {pin, good.AgentInstallationID, "other"}, {pin, "", good.EngineID}} {
		if !errors.Is(v.Verify(bad), ErrMismatch) {
			t.Fatalf("accepted %+v", bad)
		}
	}
	next := v
	next.Host = "new.example"
	if !errors.Is(r.Update(v, next, false), ErrUnconfirmed) {
		t.Fatal("unconfirmed update")
	}
	if e = r.Update(v, next, true); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(r.Update(v, next, true), ErrConflict) {
		t.Fatal("stale update accepted")
	}
	changed := next
	changed.ID = "other"
	if !errors.Is(r.Update(next, changed, true), ErrConflict) {
		t.Fatal("renamed")
	}
}
func TestCorruptionFailsClosed(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":1,"targets":null}`, `{"version":1,"version":1,"targets":[]}`, `{"version":1,"targets":[],"password":"secret"}`, `{"version":1,"targets":[]} {}`, `{broken`, `{"version":2,"targets":[]}`} {
		t.Run(data, func(t *testing.T) {
			r := registry(t)
			if e := os.WriteFile(filepath.Join(r.dir, "targets.json"), []byte(data), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e := r.List(); e == nil {
				t.Fatal("accepted corrupt data")
			}
			if e := r.Add(record(), true); e == nil {
				t.Fatal("overwrote corrupt data")
			}
		})
	}
	r := registry(t)
	if e := r.Add(record(), true); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(r.dir, "targets.json"))
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	start := strings.Index(s, "    {")
	end := strings.LastIndex(s, "    }")
	if start < 0 || end < 0 {
		t.Fatal(s)
	}
	duplicate := s[:end+5] + "," + s[start:end+5] + s[end+5:]
	if e = os.WriteFile(filepath.Join(r.dir, "targets.json"), []byte(duplicate), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = r.List(); e == nil {
		t.Fatal("accepted duplicate records")
	}
}
func TestValidationAndStorage(t *testing.T) {
	r := registry(t)
	v := record()
	for _, mutate := range []func(*Record){func(x *Record) { x.KeyPath = "relative" }, func(x *Record) { x.Host = "user:password@host" }, func(x *Record) { x.HostKeySHA256 = "SHA256:bad" }, func(x *Record) { x.EngineID = "" }, func(x *Record) { x.User = "user:password" }, func(x *Record) { x.KeyPath = "/key\nPRIVATE KEY" }} {
		x := v
		mutate(&x)
		if r.Add(x, true) == nil {
			t.Fatalf("accepted %+v", x)
		}
	}
	if e := r.Add(v, true); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(r.dir, "targets.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "PRIVATE KEY") || strings.Contains(string(b), "password") {
		t.Fatal("secret persisted")
	}
	st, e := os.Stat(filepath.Join(r.dir, "targets.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
}
func TestConcurrentAdd(t *testing.T) {
	r := registry(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- r.Add(record(), true) }()
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
}
