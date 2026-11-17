package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/crypto/ssh"
)

func enrollmentRecord(t *testing.T) targetregistry.Record {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	pub, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(key)
	pin := ssh.FingerprintSHA256(pub)
	return targetregistry.Record{Revision: 1, ID: "server", Host: "host.example", Port: 22, User: "operator", KeyPath: "/home/operator/key", HostPublicKey: strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n"), HostKeySHA256: pin, AgentInstallationID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(key), AgentFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:]), RemoteSocket: "/run/agent.sock", EngineID: "11111111-1111-4111-8111-111111111111", EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Generation: 1, RevealKeyFingerprint: pin}
}

func TestEnrollmentApprovalSnapshotAndSession(t *testing.T) {
	s, err := newEnrollmentApprovals()
	if err != nil {
		t.Fatal(err)
	}
	old := enrollmentRecord(t)
	next := old
	next.Revision++
	next.Host = "next.example"
	now := time.Now()
	token, err := s.issue("session", enrollmentSnapshot{Previous: &old, Candidate: next}, now)
	if err != nil {
		t.Fatal(err)
	}
	old.Host = "substituted.example"
	next.Host = "substituted.example"
	if _, ok := s.consume("other", token, now); ok {
		t.Fatal("other session accepted")
	}
	got, ok := s.consume("session", token, now)
	if !ok || got.Previous.Host != "host.example" || got.Candidate.Host != "next.example" {
		t.Fatal("snapshot changed", got, ok)
	}
	if _, ok = s.consume("session", token, now); ok {
		t.Fatal("replay accepted")
	}
}

func TestCommitEnrollmentExactObservationAndCAS(t *testing.T) {
	r, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	original := enrollmentRecord(t)
	add := enrollmentSnapshot{Candidate: original}
	changed := original
	changed.Host = "other.example"
	if commitEnrollment(context.Background(), r, add, changed) == nil {
		t.Fatal("substituted observation accepted")
	}
	if err := commitEnrollment(context.Background(), r, add, original); err != nil {
		t.Fatal(err)
	}
	next := original
	next.Revision++
	next.Host = "next.example"
	approved := enrollmentSnapshot{Previous: &original, Candidate: next}
	concurrent := original
	concurrent.Revision++
	concurrent.Host = "concurrent.example"
	if err := r.Update(original, concurrent, targetregistry.ReviewedUpdate{Previous: original, Next: concurrent}); err != nil {
		t.Fatal(err)
	}
	if commitEnrollment(context.Background(), r, approved, next) == nil {
		t.Fatal("stale prior accepted")
	}
	got, err := r.Get(original.ID)
	if err != nil || got != concurrent {
		t.Fatal("concurrent record changed", got, err)
	}
}

func TestEnrollmentApprovalExpiryAndRace(t *testing.T) {
	s, _ := newEnrollmentApprovals()
	now := time.Now()
	rec := enrollmentRecord(t)
	token, err := s.issue("session", enrollmentSnapshot{Candidate: rec}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.consume("session", token, now.Add(2*time.Minute)); ok {
		t.Fatal("expired accepted")
	}
	token, err = s.issue("session", enrollmentSnapshot{Candidate: rec}, now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var wins atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.consume("session", token, now); ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("wins: %d", wins.Load())
	}
	rec.Revision = 2
	if _, err = s.issue("session", enrollmentSnapshot{Candidate: rec}, now); err == nil {
		t.Fatal("invalid revision accepted")
	}
}
