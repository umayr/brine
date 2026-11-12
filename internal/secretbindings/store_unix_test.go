//go:build unix

package secretbindings

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, string, *Store) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(home, ".brine-secret-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	root := filepath.Join(base, "private")
	workspace := filepath.Join(base, "workspace")
	for _, p := range []string{root, workspace} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return root, workspace, s
}
func TestStoreDraftPreviewActiveIsolation(t *testing.T) {
	root, workspace, s := fixture(t)
	doc := []byte(validDoc)
	if err := s.SaveDraft("deployment-a", "demo", "lab-a", doc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Active("deployment-a", "demo", "lab-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active before approval: %v", err)
	}
	if _, err := s.Draft("deployment-b", "demo", "lab-a"); err == nil {
		t.Fatal("cross-deployment read")
	}
	proof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	plan := []byte("{ \"changes\": [] }")
	if err := s.SavePreview("deployment-a", "demo", "lab-a", doc, proof, token, plan, 1); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Preview("deployment-a", "demo", "lab-a")
	if err != nil || !bytes.Equal(snapshot.Plan, plan) || snapshot.Exit != 1 {
		t.Fatal("preview not retained")
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(serialized), proof) || strings.Contains(string(serialized), "db-prod") {
		t.Fatal("private preview serialized")
	}
	if err := s.ConfirmPreview("deployment-a", "demo", "lab-a", proof, token, []byte(`{}`), 1); !errors.Is(err, ErrStale) {
		t.Fatalf("changed plan: %v", err)
	}
	if err := s.ConfirmPreview("deployment-a", "demo", "lab-a", proof, token, plan, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("preview not invalidated: %v", err)
	}
	if err := s.SavePreview("deployment-a", "demo", "lab-a", doc, proof, token, plan, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmPreview("deployment-a", "demo", "lab-a", proof, token, plan, 1); err != nil {
		t.Fatal(err)
	}
	active, err := s.Active("deployment-a", "demo", "lab-a")
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := s.SecretEnabled("deployment-a")
	if err != nil || !enabled {
		t.Fatalf("marker: %v %v", enabled, err)
	}
	reopened, err := Open(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if enabled, err := reopened.SecretEnabled("deployment-a"); err != nil || !enabled {
		t.Fatal("marker not persistent")
	}
	newer := strings.Replace(validDoc, `"revision":"r2"`, `"revision":"r3"`, 1)
	if err := s.SaveDraft("deployment-a", "demo", "lab-a", []byte(newer)); err != nil {
		t.Fatal(err)
	}
	after, err := s.Active("deployment-a", "demo", "lab-a")
	if err != nil || !bytes.Equal(active, after) {
		t.Fatalf("draft altered active: %v", err)
	}
	if err := s.ConfirmPreview("deployment-a", "demo", "lab-a", proof, token, plan, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("stale preview: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "draft.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state in workspace")
	}
	for _, path := range []string{filepath.Join(root, "deployment-a"), filepath.Join(root, "deployment-a", "draft.json"), filepath.Join(root, "deployment-a", "active.json"), filepath.Join(root, "deployment-a", "secret-enabled")} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if fi.IsDir() {
			want = 0700
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("bad permissions: %o", fi.Mode().Perm())
		}
	}
}
func TestStoreProofKeyAndAllocation(t *testing.T) {
	_, _, s := fixture(t)
	a, err := s.ProofKey("a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ProofKey("a")
	if err != nil || !bytes.Equal(a, again) || len(a) != 32 {
		t.Fatal("key not persisted")
	}
	b, err := s.ProofKey("b")
	if err != nil || bytes.Equal(a, b) {
		t.Fatal("keys not isolated")
	}
	p, err := s.AllocateProofOutput("a")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.AllocateProofOutput("a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Path() == q.Path() {
		t.Fatal("output collision")
	}
	if _, err = os.Lstat(p.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("output already exists")
	}
	if err = os.WriteFile(p.Path(), []byte(`{"proof":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Read(); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestStoreRejectsUnsafePaths(t *testing.T) {
	root, workspace, s := fixture(t)
	if _, err := Open(workspace, workspace); err == nil {
		t.Fatal("workspace allowed")
	}
	if err := s.SaveDraft("../escape", "demo", "lab-a", []byte(validDoc)); err == nil {
		t.Fatal("traversal allowed")
	}
	if err := os.Symlink(workspace, filepath.Join(root, "bad")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDraft("bad", "demo", "lab-a", []byte(validDoc)); err == nil {
		t.Fatal("deployment symlink allowed")
	}
	if err := s.SaveDraft("good", "demo", "lab-a", []byte(validDoc)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "good", "draft.json")
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Draft("good", "demo", "lab-a"); err == nil {
		t.Fatal("unsafe file mode allowed")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(p, filepath.Join(root, "good", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Draft("good", "demo", "lab-a"); err == nil {
		t.Fatal("hardlink allowed")
	}
}
func TestCoordinatorSerializesDeployment(t *testing.T) {
	var c Coordinator
	unlock := c.Lock("a")
	done := make(chan struct{})
	go func() { defer close(done); u := c.Lock("a"); u() }()
	select {
	case <-done:
		t.Fatal("same deployment not locked")
	case <-time.After(20 * time.Millisecond):
	}
	other := c.Lock("b")
	other()
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lock not released")
	}
}
