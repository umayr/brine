//go:build linux

package auth

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Linux POSIX ACL xattr v2: owner, named synthetic UID, group, mask, other.
// A zero access mask preserves0600/0700 mode bits but still leaves an extended
// ACL; a default mask of7 can pass grants to later children regardless of the
// current directory's private access mode. We conservatively reject both.
func credentialLinuxACL(mask uint16, directory bool) []byte {
	type entry struct {
		tag, perm uint16
		id        uint32
	}
	owner := uint16(6)
	if directory {
		owner = 7
	}
	entries := []entry{{1, owner, ^uint32(0)}, {2, 7, uint32(os.Geteuid()) + 1}, {4, 0, ^uint32(0)}, {16, mask, ^uint32(0)}, {32, 0, ^uint32(0)}}
	b := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(b, 2)
	for i, e := range entries {
		at := 4 + i*8
		binary.LittleEndian.PutUint16(b[at:], e.tag)
		binary.LittleEndian.PutUint16(b[at+2:], e.perm)
		binary.LittleEndian.PutUint32(b[at+4:], e.id)
	}
	return b
}

func setCredentialLinuxACL(t *testing.T, path, name string, acl []byte) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err = unix.Fsetxattr(fd, name, acl, 0); err != nil {
		t.Fatalf("could not provision synthetic Linux ACL: %v", err)
	}
	got := make([]byte, len(acl))
	n, err := unix.Fgetxattr(fd, name, got)
	if err != nil || !bytes.Equal(got[:n], acl) {
		t.Fatalf("synthetic Linux ACL readback failed: %v", err)
	}
	t.Logf("injected and read back %s (%d bytes)", name, n)
}

// Prove the actual state is accepted immediately before each ACL injection.
// Otherwise an already-unsafe ancestor could make every negative case vacuous.
func credentialLinuxBaseline(t *testing.T, state string) {
	t.Helper()
	path := CredentialsPath(state)
	putCredentials(t, path, syntheticCredentials)
	c, created, p, err := LoadOrCreate(state, "", "", "")
	if err != nil || created || p != path || c.Password != "synthetic-login" || c.APIToken != "synthetic-api" {
		t.Fatalf("positive ACL-free credential baseline rejected: %v", err)
	}
	t.Log("positive baseline: actual state ancestry and credential read accepted")
}

func TestCredentialLinuxAccessAndDefaultACLBoundary(t *testing.T) {
	for _, kind := range []string{"state-access", "state-default", "parent-access", "parent-default", "file-access"} {
		t.Run(kind, func(t *testing.T) {
			base := credentialTestDir(t)
			state := filepath.Join(base, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			path := CredentialsPath(state)
			credentialLinuxBaseline(t, state)
			if kind != "file-access" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "state-access":
				setCredentialLinuxACL(t, state, "system.posix_acl_access", credentialLinuxACL(0, true))
			case "state-default":
				setCredentialLinuxACL(t, state, "system.posix_acl_default", credentialLinuxACL(7, true))
			case "parent-access":
				setCredentialLinuxACL(t, base, "system.posix_acl_access", credentialLinuxACL(0, true))
			case "parent-default":
				setCredentialLinuxACL(t, base, "system.posix_acl_default", credentialLinuxACL(7, true))
			case "file-access":
				setCredentialLinuxACL(t, path, "system.posix_acl_access", credentialLinuxACL(0, false))
			}
			c, created, p, err := LoadOrCreate(state, "", "", "")
			if err == nil || c != (Credentials{}) || created || p != "" {
				t.Fatal("extended POSIX ACL accepted")
			}
			if kind != "file-access" {
				if _, err = os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("credential document created under ACL-bearing ancestry")
				}
			} else {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != syntheticCredentials {
					t.Fatal("ACL-bearing credential file changed")
				}
			}
			t.Log("negative boundary: ACL rejected without publishing or changing credential bytes")
		})
	}
}

func TestCredentialLinuxNewDescriptorACLRecheck(t *testing.T) {
	state := credentialTestDir(t)
	credentialLinuxBaseline(t, state)
	if err := os.Remove(CredentialsPath(state)); err != nil {
		t.Fatal(err)
	}
	s, err := openCredentialState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// Exercise the same temporary writer on an ACL-free new descriptor first.
	baseline, err := os.OpenFile(filepath.Join(state, "synthetic-baseline-temp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeCredentialTemporary(baseline, []byte("synthetic-baseline")); err != nil {
		baseline.Close()
		t.Fatalf("positive new-descriptor write baseline rejected: %v", err)
	}
	if err = baseline.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("positive baseline: ACL-free new-descriptor write accepted")
	setCredentialLinuxACL(t, state, "system.posix_acl_default", credentialLinuxACL(7, true))
	fd, err := unix.Openat(int(s.dir.Fd()), "synthetic-empty-temp", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "synthetic empty temporary credential")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&0777 != 0600 || st.Size != 0 || credentialACLSafe(fd) {
		t.Fatal("inherited ACL on empty0600 descriptor not detected")
	}
	if err = writeCredentialTemporary(f, []byte("synthetic-must-not-be-written")); err == nil {
		t.Fatal("new ACL-bearing descriptor received sensitive bytes")
	}
	if unix.Fstat(fd, &st) != nil || st.Size != 0 {
		t.Fatal("sensitive bytes written before ACL rejection")
	}
	if err = s.write([]byte("synthetic-must-not-be-written")); err == nil {
		t.Fatal("ACL-mutated state accepted")
	}
	if _, err = os.Stat(CredentialsPath(state)); !os.IsNotExist(err) {
		t.Fatal("credential bytes published")
	}
	t.Log("negative boundary: inherited descriptor ACL rejected before write; size remains zero; state recheck rejects publication")
}
