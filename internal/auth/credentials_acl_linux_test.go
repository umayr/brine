//go:build linux

package auth

import (
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
		t.Fatal("could not provision synthetic Linux ACL")
	}
}

func TestCredentialLinuxAccessAndDefaultACLBoundary(t *testing.T) {
	for _, kind := range []string{"state-access", "state-default", "parent-default", "file-access"} {
		t.Run(kind, func(t *testing.T) {
			base := credentialTestDir(t)
			state := filepath.Join(base, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			path := CredentialsPath(state)
			switch kind {
			case "state-access":
				setCredentialLinuxACL(t, state, "system.posix_acl_access", credentialLinuxACL(0, true))
			case "state-default":
				setCredentialLinuxACL(t, state, "system.posix_acl_default", credentialLinuxACL(7, true))
			case "parent-default":
				setCredentialLinuxACL(t, base, "system.posix_acl_default", credentialLinuxACL(7, true))
			case "file-access":
				putCredentials(t, path, syntheticCredentials)
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
			}
		})
	}
}

func TestCredentialLinuxNewDescriptorACLRecheck(t *testing.T) {
	state := credentialTestDir(t)
	s, err := openCredentialState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
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
}
