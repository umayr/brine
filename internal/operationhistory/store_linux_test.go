//go:build linux

package operationhistory

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func privateRoots(t *testing.T) (string, string, string) {
	t.Helper()
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	home, e = filepath.EvalSymlinks(home)
	if e != nil {
		t.Fatal(e)
	}
	// Deliberately not t.TempDir (/tmp ancestors are unsafe). This is synthetic
	// offline state under a canonical native private HOME, never an engine root.
	parent, e := os.MkdirTemp(home, ".brine-operation-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	paths := []string{filepath.Join(parent, "history"), filepath.Join(parent, "data"), filepath.Join(parent, "coord")}
	for _, p := range paths {
		if e = os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	return paths[0], paths[1], paths[2]
}
func TestLinuxPositiveLockRestartUnknownAndTerminalRetention(t *testing.T) {
	root, data, coord := privateRoots(t)
	s, e := Open(root, data, coord)
	if e != nil {
		t.Fatalf("positive open requires safe native HOME: %v", e)
	}
	if other, e := Open(root, data, coord); e == nil {
		other.Close()
		t.Fatal("second owning server")
	}
	accepted, e := s.Begin(testIntent())
	if e != nil {
		t.Fatal(e)
	}
	running, e := s.Begin(testIntent())
	if e != nil || running.Started() != nil {
		t.Fatal("running")
	}
	complete, e := s.Begin(testIntent())
	if e != nil || complete.Started() != nil || complete.Terminal(terminal(true)) != nil {
		t.Fatal("terminal")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(root, data, coord)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	for _, id := range []string{accepted.ID(), running.ID()} {
		got, e := reopened.Get(id)
		if e != nil || got.Phase != "unknown" || got.EndObservedAt != nil || got.DillOperationID != nil || got.Invocation != nil || got.RecoveryObservedAt == nil {
			t.Fatalf("restart fabricated completion: %+v %v", got, e)
		}
	}
	got, e := reopened.Get(complete.ID())
	if e != nil || got.Status != "failed" || got.Invocation.Receipt.Outcome != "succeeded" || got.Invocation.Receipt.Effects != "complete" || got.RecoveryObservedAt != nil {
		t.Fatalf("terminal lost: %+v %v", got, e)
	}
	names, _ := os.ReadDir(root)
	if len(names) != 3 {
		t.Fatal("replay/extra state")
	}
}
func TestLinuxUnsafeStateAfterPositiveControl(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink", "hardlink", "mode", "pending", "root-mode", "ancestor-mode"} {
		t.Run(kind, func(t *testing.T) {
			root, data, coord := privateRoots(t)
			s, e := Open(root, data, coord)
			if e != nil {
				t.Fatal("positive control", e)
			}
			r, e := s.Begin(testIntent())
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Get(r.ID()); e != nil {
				t.Fatal(e)
			}
			s.Close()
			path := filepath.Join(root, r.ID()+".json")
			switch kind {
			case "corrupt":
				e = os.WriteFile(path, []byte(`PRIVATE_SOURCE_STDERR`), 0600)
			case "symlink":
				saved := filepath.Join(data, "saved-record")
				e = os.Rename(path, saved)
				if e == nil {
					e = os.Symlink(saved, path)
				}
			case "hardlink":
				e = os.Link(path, filepath.Join(data, "linked"))
			case "mode":
				e = os.Chmod(path, 0640)
			case "pending":
				e = os.WriteFile(filepath.Join(root, ".pending-uncertain"), []byte("{}"), 0600)
			case "root-mode":
				e = os.Chmod(root, 0750)
			case "ancestor-mode":
				e = os.Chmod(filepath.Dir(root), 0770)
			}
			if e != nil {
				t.Fatal(e)
			}
			if opened, e := Open(root, data, coord); e == nil {
				opened.Close()
				t.Fatal("unsafe state accepted", kind)
			}
			if _, e = os.Lstat(path); e != nil {
				t.Fatal("state deleted", e)
			}
		})
	}
}
func TestLinuxAccessAndDefaultACLAfterPositiveControl(t *testing.T) {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		t.Run(name, func(t *testing.T) {
			root, data, coord := privateRoots(t)
			s, e := Open(root, data, coord)
			if e != nil {
				t.Fatal("positive", e)
			}
			s.Close()
			// Extended ACL retaining mode0700: named user grants no access, but any
			// nontrivial access/default ACL is outside the qualified storage contract.
			acl := make([]byte, 4+5*8)
			binary.LittleEndian.PutUint32(acl, 2)
			for i, entry := range []struct {
				tag, perm uint16
				id        uint32
			}{{1, 7, ^uint32(0)}, {2, 0, uint32(os.Geteuid() + 1)}, {4, 0, ^uint32(0)}, {16, 0, ^uint32(0)}, {32, 0, ^uint32(0)}} {
				off := 4 + i*8
				binary.LittleEndian.PutUint16(acl[off:], entry.tag)
				binary.LittleEndian.PutUint16(acl[off+2:], entry.perm)
				binary.LittleEndian.PutUint32(acl[off+4:], entry.id)
			}
			if e = unix.Setxattr(root, name, acl, 0); e != nil {
				t.Fatalf("ACL injection unavailable (not acceptance evidence): %v", e)
			}
			fi, e := os.Stat(root)
			if e != nil || fi.Mode().Perm() != 0700 {
				t.Fatal("ACL injection changed mode; negative would be vacuous", e)
			}
			if opened, e := Open(root, data, coord); e == nil {
				opened.Close()
				t.Fatal("ACL accepted")
			}
		})
	}
}
