//go:build linux || darwin

package sqlitestore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func persistentFixture(t *testing.T) string {
	t.Helper()
	home, e := os.UserHomeDir()
	if e != nil {
		t.Skip(e)
	}
	t.Setenv("TMPDIR", home)
	d := t.TempDir()
	if e = os.Chmod(d, 0700); e != nil {
		t.Fatal(e)
	}
	f, e := openHistoryDir(d)
	if e != nil {
		t.Skipf("unsafe fixture: %v", e)
	}
	f.Close()
	return d
}
func TestPersistentLockRestart(t *testing.T) {
	d := persistentFixture(t)
	s, e := OpenPersistent(d, true)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := OpenPersistent(d, false); e == nil {
		other.Close()
		t.Fatal("contender admitted")
	}
	r := Record{Receipt: time.Now(), Target: "synthetic", Gauge: "count", Value: 0}
	if e = s.Append(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Close() }()
	}
	wg.Wait()
	s, e = OpenPersistent(d, false)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	rows, e := s.Read(context.Background(), 10)
	if e != nil || len(rows) != 1 || rows[0].Target != r.Target {
		t.Fatalf("restart: %#v %v", rows, e)
	}
}
func TestPersistentRejectPreserves(t *testing.T) {
	d := persistentFixture(t)
	p := filepath.Join(d, HistoryFilename)
	data := []byte("foreign synthetic content")
	if e := os.WriteFile(p, data, 0600); e != nil {
		t.Fatal(e)
	}
	for _, side := range []string{"", "-journal", "-wal", "-shm"} {
		if side != "" {
			if e := os.WriteFile(p+side, data, 0600); e != nil {
				t.Fatal(e)
			}
		}
		if s, e := OpenPersistent(d, false); e == nil {
			s.Close()
			t.Fatal("accepted foreign")
		}
		got, e := os.ReadFile(p)
		if e != nil || string(got) != string(data) {
			t.Fatal("database modified")
		}
		if side != "" {
			got, e = os.ReadFile(p + side)
			if e != nil || string(got) != string(data) {
				t.Fatal("sidecar modified")
			}
			if e = os.Remove(p + side); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := os.Link(p, p+"-hardlink"); e != nil {
		t.Fatal(e)
	}
	if s, e := OpenPersistent(d, false); e == nil {
		s.Close()
		t.Fatal("accepted hardlink")
	}
}

func TestDirectoryOwnershipFacts(t *testing.T) {
	dir := uint32(unix.S_IFDIR)
	for _, tc := range []struct {
		mode, uid, euid uint32
		private, want   bool
	}{
		{dir | 0755, 0, 123, false, true}, {dir | 0755, 123, 123, false, true}, {dir | 0755, 124, 123, false, false},
		{dir | 0700, 123, 123, true, true}, {dir | 0700, 0, 123, true, false}, {dir | 01777, 0, 123, false, false},
	} {
		if got := trustedDirectory(tc.mode, tc.uid, tc.euid, tc.private); got != tc.want {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}
func TestPersistentRecheckReplacement(t *testing.T) {
	d := persistentFixture(t)
	p := filepath.Join(d, HistoryFilename)
	if err := os.WriteFile(p, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := openHistoryDir(d)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	original, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = recheckPersistent(d, p, held, original); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = recheckPersistent(d, p, held, original); err == nil {
		t.Fatal("replacement accepted")
	}
}
func TestPersistentValidSidecarsPreserved(t *testing.T) {
	d := persistentFixture(t)
	s, err := OpenPersistent(d, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, HistoryFilename)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		side := p + suffix
		payload := []byte("synthetic sidecar")
		if err = os.WriteFile(side, payload, 0600); err != nil {
			t.Fatal(err)
		}
		if s, err = OpenPersistent(d, false); err == nil {
			s.Close()
			t.Fatal("accepted sidecar")
		}
		after, e := os.ReadFile(p)
		if e != nil || string(after) != string(before) {
			t.Fatal("db changed")
		}
		after, e = os.ReadFile(side)
		if e != nil || string(after) != string(payload) {
			t.Fatal("sidecar changed")
		}
		if e = os.Remove(side); e != nil {
			t.Fatal(e)
		}
	}
}
func TestPersistentAbruptExit(t *testing.T) {
	if d := os.Getenv("PERSISTENT_SYNTHETIC_CHILD"); d != "" {
		s, e := OpenPersistent(d, true)
		if e != nil {
			os.Exit(2)
		}
		if e = s.Append(context.Background(), Record{Receipt: time.Unix(1700000000, 0), Target: "synthetic", Gauge: "count"}); e != nil {
			os.Exit(3)
		}
		os.Exit(23)
	}
	d := persistentFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistentAbruptExit$")
	cmd.Env = append(os.Environ(), "PERSISTENT_SYNTHETIC_CHILD="+d)
	out, err := cmd.CombinedOutput()
	var ex *exec.ExitError
	if !errors.As(err, &ex) || ex.ExitCode() != 23 {
		t.Fatalf("child %v %s", err, out)
	}
	s, err := OpenPersistent(d, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.Read(context.Background(), 10)
	if err != nil || len(rows) != 1 || rows[0].Target != "synthetic" {
		t.Fatalf("rows %v %v", rows, err)
	}
}

func TestPersistentRootReplacement(t *testing.T) {
	anchor := persistentFixture(t)
	d := filepath.Join(anchor, "history")
	if err := os.Mkdir(d, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, HistoryFilename)
	if err := os.WriteFile(p, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := openHistoryDir(d)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	original, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(d, d+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = recheckPersistent(d, p, held, original); err == nil {
		t.Fatal("replacement root accepted")
	}
}
