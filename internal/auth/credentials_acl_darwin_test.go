//go:build darwin

package auth

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func darwinCredentialACL(t *testing.T, path, entry string) {
	t.Helper()
	if err := exec.Command("/bin/chmod", "+a", entry, path).Run(); err != nil {
		t.Fatal("could not provision synthetic Darwin ACL")
	}
	t.Cleanup(func() { _ = exec.Command("/bin/chmod", "-N", path).Run() })
}

func TestCredentialDarwinACLBoundary(t *testing.T) {
	const inheritance = "everyone allow read,write,execute,delete,add_file,add_subdirectory,delete_child,file_inherit,directory_inherit"
	for _, kind := range []string{"state-inheritance", "parent-grant", "file-grant", "deny-only"} {
		t.Run(kind, func(t *testing.T) {
			base := credentialTestDir(t)
			state := filepath.Join(base, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			path := CredentialsPath(state)
			switch kind {
			case "state-inheritance":
				darwinCredentialACL(t, state, inheritance)
			case "parent-grant":
				darwinCredentialACL(t, base, "everyone allow add_file,add_subdirectory,delete_child")
			case "file-grant":
				putCredentials(t, path, syntheticCredentials)
				darwinCredentialACL(t, path, "everyone allow read,write")
			case "deny-only":
				darwinCredentialACL(t, state, "everyone deny delete")
			}
			c, created, p, err := LoadOrCreate(state, "", "", "")
			if kind == "deny-only" {
				if err != nil || !created || p == "" || c.Password == "" {
					t.Fatal("restrictive deny-only ACL rejected")
				}
				return
			}
			if err == nil || c != (Credentials{}) || created || p != "" {
				t.Fatal("mode-private ACL grant accepted")
			}
			if kind == "file-grant" {
				b, e := os.ReadFile(path)
				if e != nil || string(b) != syntheticCredentials {
					t.Fatal("ACL-bearing file modified")
				}
			} else if _, e := os.Lstat(path); !os.IsNotExist(e) {
				t.Fatal("credential bytes published under ACL grant")
			}
		})
	}
	// Demonstrate the exact creation hazard: 0600 does not neutralize an
	// inherited Darwin allow ACL. The new-descriptor preflight must reject it
	// while the exclusively-created file is still empty, before any secret write.
	t.Run("new-file-inherited-grant", func(t *testing.T) {
		state := credentialTestDir(t)
		s, err := openCredentialState(state)
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		darwinCredentialACL(t, state, inheritance)
		fd, err := unix.Openat(int(s.dir.Fd()), "synthetic-empty-temp", unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(fd), "synthetic empty temporary credential")
		defer f.Close()
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&0777 != 0600 || st.Size != 0 || credentialACLSafe(fd) {
			t.Fatal("inherited grant on empty0600 descriptor not detected")
		}
		if err = writeCredentialTemporary(f, []byte("synthetic-must-not-be-written")); err == nil {
			t.Fatal("new ACL-bearing descriptor received sensitive bytes")
		}
		if unix.Fstat(fd, &st) != nil || st.Size != 0 {
			t.Fatal("sensitive bytes written before ACL rejection")
		}
		if err = s.write([]byte("synthetic-must-not-be-written")); err == nil {
			t.Fatal("ACL-mutated state accepted for write")
		}
		if _, err = os.Lstat(CredentialsPath(state)); !os.IsNotExist(err) {
			t.Fatal("sensitive file published")
		}
		b, err := os.ReadFile(filepath.Join(state, "synthetic-empty-temp"))
		if err != nil || len(b) != 0 {
			t.Fatal("new descriptor received sensitive bytes")
		}
	})
}

func TestCredentialDarwinACLParserFailsClosed(t *testing.T) {
	// Native packed kauth_filesec layout, independent of the runtime producer.
	blob := make([]byte, 12+44+24)
	put := binary.LittleEndian.PutUint32
	put(blob[:4], uint32(len(blob)))
	put(blob[4:8], 8)
	put(blob[8:12], 68)
	put(blob[12:16], 0x012cc16d)
	put(blob[48:52], 1)
	put(blob[72:76], 2) // one deny ACE
	if !credentialDarwinACLBytesSafe(blob) {
		t.Fatal("deny ACL parser fixture rejected")
	}
	put(blob[72:76], 1|1<<8) // inheritance-only permit must also fail
	if credentialDarwinACLBytesSafe(blob) {
		t.Fatal("inheritance-only grant accepted")
	}
	put(blob[72:76], 2)
	put(blob[8:12], 9999)
	if credentialDarwinACLBytesSafe(blob) {
		t.Fatal("out-of-bounds ACL accepted")
	}
	for n := 0; n < 12; n++ {
		if credentialDarwinACLBytesSafe(blob[:n]) {
			t.Fatal("truncated ACL accepted")
		}
	}
}
