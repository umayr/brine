//go:build unix

package secretbindings

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/store"
)

func TestPreviewKeyRotationInvalidates(t *testing.T) {
	_, _, s := fixture(t)
	doc := []byte(validDoc)
	if err := s.SaveDraft("demo", "demo", "lab-a", doc); err != nil {
		t.Fatal(err)
	}
	old, err := s.ProofKey("demo")
	if err != nil {
		t.Fatal(err)
	}
	p := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	plan := []byte(`{"changes":[]}`)
	if err = s.SavePreview("demo", "demo", "lab-a", doc, p, token, plan, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.RotateProofKey("demo"); err != nil {
		t.Fatal(err)
	}
	next, err := s.ProofKey("demo")
	if err != nil || bytes.Equal(old, next) {
		t.Fatal("key did not rotate")
	}
	if err = s.ConfirmPreview("demo", "demo", "lab-a", p, token, plan, 0); !errors.Is(err, ErrStale) {
		t.Fatal("old preview accepted")
	}
	path, err := s.ProofKeyPath("demo")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("key path not private")
	}
}
func TestUnsafeProofOutputIsNotDeleted(t *testing.T) {
	_, workspace, s := fixture(t)
	p, err := s.AllocateProofOutput("demo")
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(workspace, "victim")
	if err = os.WriteFile(victim, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(victim, p.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Read(); err == nil {
		t.Fatal("read through symlink")
	}
	if err = p.Close(); err == nil {
		t.Fatal("deleted unsafe output")
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "synthetic" {
		t.Fatal("victim changed")
	}
}
func TestReferencesNotReachableViaProjectEditor(t *testing.T) {
	root, workspace, s := fixture(t)
	st, err := store.NewFSStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", "synthetic pkl"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveDraft("demo", "demo", "lab-a", []byte(validDoc)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"draft.json", "../../private/demo/draft.json", "draft.json.pkl"} {
		if value, err := st.ReadFile("demo", name); err == nil && strings.Contains(value, "db-prod") {
			t.Fatal("reference state served via editor")
		}
	}
	if _, err = os.Stat(filepath.Join(root, "demo", "draft.json")); err != nil {
		t.Fatal(err)
	}
	// API input must contain only references; neither errors nor serialized
	// reference documents carry an uploaded value field.
	input := strings.Replace(validDoc, `"revision":"r2"`, `"revision":"r2","value":"synthetic-plaintext-canary"`, 1)
	err = s.SaveDraft("demo", "demo", "lab-a", []byte(input))
	if err == nil || strings.Contains(err.Error(), "synthetic-plaintext-canary") {
		t.Fatal("plaintext input accepted or echoed")
	}
	b, err := s.Draft("demo", "demo", "lab-a")
	if err != nil || strings.Contains(string(b), "synthetic-plaintext-canary") {
		t.Fatal("plaintext stored")
	}
}
