package secretbindings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuarantineInvalidUTF8Denied(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := NewQuarantine(dir)
	if err != nil {
		t.Fatal(err)
	}
	bad := string([]byte{0xff})
	if _, err = q.Begin(bad); err == nil {
		t.Fatal("invalid UTF8 admitted")
	}
	if q.Check(bad) == nil || q.Complete(bad, "token") == nil {
		t.Fatal("invalid identity accepted")
	}
	rows, err := q.List()
	if err != nil || len(rows) != 0 {
		t.Fatal("invalid identity changed ledger")
	}
}

func TestQuarantineDoubleFileLossCannotReinitialize(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := NewQuarantine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.Begin("a"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ledgerName, anchorName} {
		if err = os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = NewQuarantine(dir); err == nil {
		t.Fatal("lost state reinitialized")
	}
	if err = q.Check("a"); err == nil {
		t.Fatal("lost state permitted mutation")
	}
}

func TestQuarantineCorruptCanonicalStateDenied(t *testing.T) {
	for _, body := range []string{"null", `{"a":"bad"}`, `{} {}`, `{"a":null}`} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		q, err := NewQuarantine(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, ledgerName), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if q.Check("a") == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
