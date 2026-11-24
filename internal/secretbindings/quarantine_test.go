package secretbindings

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestQuarantineRestartAndCAS(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := NewQuarantine(dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := q.Begin("a")
	if err != nil {
		t.Fatal(err)
	}
	q2, err := NewQuarantine(dir)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Check("a") == nil {
		t.Fatal("restart lost quarantine")
	}
	if _, err = q2.Begin("a"); err == nil {
		t.Fatal("overlap accepted")
	}
	if q2.Complete("a", "wrong") == nil {
		t.Fatal("wrong token accepted")
	}
	if err = q2.Complete("a", token); err != nil {
		t.Fatal(err)
	}
	if err = q.Check("a"); err != nil {
		t.Fatal(err)
	}
}
func TestQuarantineCorruptAndLost(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "lost"}[lost], func(t *testing.T) {
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
			p := filepath.Join(dir, ledgerName)
			if lost {
				err = os.Remove(p)
			} else {
				err = os.WriteFile(p, []byte("{"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if q.Check("a") == nil {
				t.Fatal("accepted bad ledger")
			}
			if _, err = NewQuarantine(dir); err == nil {
				t.Fatal("reinitialized bad ledger")
			}
		})
	}
}
func TestQuarantineSpecialFiles(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "fifo", true: "symlink"}[symlink], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := NewQuarantine(dir); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, ledgerName)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			var err error
			if symlink {
				err = os.Symlink("/dev/null", p)
			} else {
				err = unix.Mkfifo(p, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = NewQuarantine(dir); err == nil {
				t.Fatal("accepted special file")
			}
		})
	}
}
