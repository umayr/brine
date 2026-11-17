package targetregistry

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	h := sha256.Sum256(key)
	return Record{Revision: 1, RemoteSocket: "/run/brine.sock", AgentPublicKey: base64.RawURLEncoding.EncodeToString(key), AgentFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:]), EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Generation: 1, RevealKeyFingerprint: pin, ID: "server", Host: "host.example", Port: 22, User: "operator", KeyPath: "/home/operator/.ssh/id_ed25519", HostPublicKey: testKey, HostKeySHA256: pin, AgentInstallationID: hex.EncodeToString(h[:]), EngineID: "11111111-1111-4111-8111-111111111111"}
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
	if !errors.Is(r.Add(v, Record{}), ErrUnconfirmed) {
		t.Fatal("unconfirmed add")
	}
	if e := r.Add(v, v); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(r.Add(v, v), ErrConflict) {
		t.Fatal("duplicate accepted")
	}
	got, e := r.Get(v.ID)
	if e != nil || got != v {
		t.Fatal(got, e)
	}
	good := v
	if e = v.Verify(good); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Record){func(r *Record) { r.HostKeySHA256 = "bad" }, func(r *Record) { r.AgentInstallationID = "other" }, func(r *Record) { r.EngineID = "other" }} {
		bad := good
		mutate(&bad)
		if !errors.Is(v.Verify(bad), ErrMismatch) {
			t.Fatal("accepted mismatch")
		}
	}
	next := v
	next.Host = "new.example"
	next.Revision++
	if !errors.Is(r.Update(v, next, ReviewedUpdate{}), ErrUnconfirmed) {
		t.Fatal("unconfirmed update")
	}
	if e = r.Update(v, next, ReviewedUpdate{v, next}); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(r.Update(v, next, ReviewedUpdate{v, next}), ErrConflict) {
		t.Fatal("stale update accepted")
	}
	changed := next
	changed.ID = "other"
	changed.Revision++
	if !errors.Is(r.Update(next, changed, ReviewedUpdate{next, changed}), ErrConflict) {
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
			if e := r.Add(record(), record()); e == nil {
				t.Fatal("overwrote corrupt data")
			}
		})
	}
	r := registry(t)
	if e := r.Add(record(), record()); e != nil {
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
		if r.Add(x, x) == nil {
			t.Fatalf("accepted %+v", x)
		}
	}
	if e := r.Add(v, v); e != nil {
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
		go func() { defer wg.Done(); errs <- r.Add(record(), record()) }()
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
