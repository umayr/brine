//go:build linux || darwin

package auth

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func putCredentials(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

const syntheticCredentials = `{"user":"admin","password":"synthetic-login","api_token":"synthetic-api"}`

func TestCredentialStateRejectsUnsafeFilesBeforeReading(t *testing.T) {
	for _, kind := range []string{"symlink", "symlink-missing-secret", "hardlink", "directory", "fifo", "permissive", "malformed", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			base := credentialTestDir(t)
			state := filepath.Join(base, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, "external-synthetic-login")
			original := syntheticCredentials
			if kind == "symlink-missing-secret" {
				original = `{"password":"synthetic-login"}`
			}
			putCredentials(t, target, original)
			// A symlink must never cause either an external read or chmod/write.
			if err := os.Chmod(target, 0644); err != nil {
				t.Fatal(err)
			}
			path := CredentialsPath(state)
			var err error
			switch kind {
			case "symlink", "symlink-missing-secret":
				err = os.Symlink(target, path)
			case "hardlink":
				if err = os.Chmod(target, 0600); err == nil {
					err = os.Link(target, path)
				}
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = unix.Mkfifo(path, 0600)
			case "permissive":
				putCredentials(t, path, original)
				err = os.Chmod(path, 0644)
			case "malformed":
				putCredentials(t, path, `{"password":"synthetic-parse-canary",`)
			case "oversize":
				putCredentials(t, path, strings.Repeat("synthetic-parse-canary", 4096))
			}
			if err != nil {
				t.Fatal(err)
			}
			c, created, p, err := LoadOrCreate(state, "", "", "")
			if err == nil || c != (Credentials{}) || created || p != "" {
				t.Fatal("unsafe credential file accepted")
			}
			if strings.Contains(err.Error(), base) || strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("unsafe error disclosed input or path")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != original {
				t.Fatal("external target modified")
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0644)
			if kind == "hardlink" {
				want = 0600
			}
			if info.Mode().Perm() != want {
				t.Fatal("external target chmodded")
			}
		})
	}
}

func TestCredentialStateRejectsUnsafeDirectories(t *testing.T) {
	for _, kind := range []string{"state-symlink", "parent-symlink", "writable-parent", "permissive-state", "sticky-parent"} {
		t.Run(kind, func(t *testing.T) {
			base := credentialTestDir(t)
			parent := filepath.Join(base, "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(parent, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			putCredentials(t, CredentialsPath(state), syntheticCredentials)
			path := state
			switch kind {
			case "state-symlink":
				path = filepath.Join(parent, "alias")
				if err := os.Symlink(state, path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				alias := filepath.Join(base, "alias")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "state")
			case "writable-parent":
				if err := os.Chmod(parent, 0777); err != nil {
					t.Fatal(err)
				}
			case "permissive-state":
				if err := os.Chmod(state, 0755); err != nil {
					t.Fatal(err)
				}
			case "sticky-parent":
				if err := os.Chmod(parent, os.ModeSticky|0777); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, err := LoadOrCreate(path, "", "", ""); err == nil {
				t.Fatal("unsafe state ancestry accepted")
			}
		})
	}
	t.Run("platform-temporary-ancestry", func(t *testing.T) {
		if runtime.GOOS == "linux" {
			info, err := os.Stat("/tmp")
			if err != nil || info.Mode().Perm()&0022 == 0 {
				t.Skip("/tmp is not writable on this host")
			}
			path := filepath.Join("/tmp", "brine-unsafe-not-created")
			if _, _, _, err := LoadOrCreate(path, "", "", ""); err == nil {
				t.Fatal("writable /tmp accepted")
			}
		} else {
			info, err := os.Lstat("/var")
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Skip("/var is not an alias on this host")
			}
			if _, _, _, err := LoadOrCreate("/var/brine-unsafe-not-created", "", "", ""); err == nil {
				t.Fatal("macOS /var alias followed")
			}
		}
	})
}

func TestCredentialStateOwnershipChecks(t *testing.T) {
	st := unix.Stat_t{Mode: unix.S_IFREG | 0600, Uid: uint32(os.Geteuid()), Nlink: 1}
	if !credentialFileSafe(&st) {
		t.Fatal("normal file rejected")
	}
	st.Uid++
	if credentialFileSafe(&st) {
		t.Fatal("foreign ownership accepted")
	}
	st.Uid--
	st.Nlink++
	if credentialFileSafe(&st) {
		t.Fatal("multiple links accepted")
	}
}

func TestCredentialMissingSecretFillAndOverrides(t *testing.T) {
	for _, document := range []string{`{"password":"synthetic-password"}`, `{"api_token":"synthetic-token"}`} {
		state := credentialTestDir(t)
		path := CredentialsPath(state)
		putCredentials(t, path, document)
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		c, created, _, err := LoadOrCreate(state, "admin", "", "")
		if err != nil || created || c.Password == "" || c.APIToken == "" {
			t.Fatal("missing secret not filled")
		}
		var old Credentials
		if json.Unmarshal([]byte(document), &old) != nil {
			t.Fatal("bad fixture")
		}
		if old.Password != "" && c.Password != old.Password || old.APIToken != "" && c.APIToken != old.APIToken {
			t.Fatal("existing secret changed")
		}
		after, err := os.Stat(path)
		if err != nil || os.SameFile(before, after) {
			t.Fatal("fill was not atomic replacement")
		}
		again, _, _, err := LoadOrCreate(state, "admin", "", "")
		if err != nil || again != c {
			t.Fatal("filled credentials not reused")
		}
		overridden, _, _, err := LoadOrCreate(state, "admin", "explicit-password", "")
		if err != nil || overridden.Password != "explicit-password" || overridden.APIToken != c.APIToken {
			t.Fatal("partial override broken")
		}
		again, _, _, err = LoadOrCreate(state, "admin", "", "")
		if err != nil || again != c {
			t.Fatal("complete stored credentials unexpectedly overwritten")
		}
	}
	// Both explicit secrets bypass even a deliberately unsafe state path.
	unsafe := filepath.Join(credentialTestDir(t), "link")
	if err := os.Symlink("/does-not-exist", unsafe); err != nil {
		t.Fatal(err)
	}
	c, created, path, err := LoadOrCreate(unsafe, "me", "pw", "tok")
	if err != nil || created || path != "" || c.Password != "pw" {
		t.Fatal("explicit credentials touched storage")
	}
}

func TestCredentialConcurrentCreateAndFill(t *testing.T) {
	for _, fill := range []bool{false, true} {
		state := filepath.Join(credentialTestDir(t), "state")
		if fill {
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			putCredentials(t, CredentialsPath(state), `{"password":"synthetic-existing"}`)
		}
		const count = 16
		var wg sync.WaitGroup
		results := make([]Credentials, count)
		errs := make([]error, count)
		created := make([]bool, count)
		for i := range results {
			wg.Add(1)
			go func(i int) { defer wg.Done(); results[i], created[i], _, errs[i] = LoadOrCreate(state, "", "", "") }(i)
		}
		wg.Wait()
		n := 0
		for i := range results {
			if errs[i] != nil || results[i] != results[0] {
				t.Fatal("concurrent callers disagreed or failed")
			}
			if created[i] {
				n++
			}
		}
		if (!fill && n != 1) || (fill && n != 0) {
			t.Fatal("incorrect creation count")
		}
		entries, err := os.ReadDir(state)
		if err != nil || len(entries) != 1 || entries[0].Name() != "credentials.json" {
			t.Fatal("temporary credential material retained")
		}
	}
}

// Exercise the OS lock across processes as well as goroutines. The child only
// writes its result to a private test fixture, never to test/service output.
func TestCredentialProcessLock(t *testing.T) {
	if state := os.Getenv("BRINE_TEST_CREDENTIAL_PROCESS_STATE"); state != "" {
		c, _, _, err := LoadOrCreate(state, "", "", "")
		if err != nil {
			t.Fatal("child credential resolution failed")
		}
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal("child encode failed")
		}
		if err := os.WriteFile(os.Getenv("BRINE_TEST_CREDENTIAL_PROCESS_RESULT"), b, 0600); err != nil {
			t.Fatal("child result failed")
		}
		return
	}
	base := credentialTestDir(t)
	state := filepath.Join(base, "state")
	const count = 4
	commands := make([]*exec.Cmd, count)
	for i := range commands {
		result := filepath.Join(base, string(rune('a'+i)))
		cmd := exec.Command(os.Args[0], "-test.run=^TestCredentialProcessLock$", "-test.count=1")
		cmd.Env = append(os.Environ(), "BRINE_TEST_CREDENTIAL_PROCESS_STATE="+state, "BRINE_TEST_CREDENTIAL_PROCESS_RESULT="+result)
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal("child start failed")
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal("child failed")
		}
	}
	var expected []byte
	for i := range commands {
		b, err := os.ReadFile(filepath.Join(base, string(rune('a'+i))))
		if err != nil {
			t.Fatal("child result missing")
		}
		if i == 0 {
			expected = b
		} else if string(b) != string(expected) {
			t.Fatal("processes generated different credentials")
		}
	}
}

func TestCredentialDescriptorPinsDirectoryAndRejectsChangedEntry(t *testing.T) {
	base := credentialTestDir(t)
	path := filepath.Join(base, "state")
	s, err := openCredentialState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if _, exists, err := s.read(); err != nil || exists {
		t.Fatal("new state not empty")
	}
	// A path replacement after traversal cannot redirect writes.
	moved := filepath.Join(base, "moved")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(base, "external")
	if err := os.Mkdir(external, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]byte(syntheticCredentials)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(CredentialsPath(external)); !os.IsNotExist(err) {
		t.Fatal("write followed replaced directory")
	}
	// Keep operating through the pinned descriptor, then inject a target swap
	// between read and replace. The external file must remain untouched.
	if _, _, err := s.read(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	putCredentials(t, target, "synthetic-external-target")
	if err := os.Remove(CredentialsPath(moved)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, CredentialsPath(moved)); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]byte("replacement")); err == nil {
		t.Fatal("changed entry replaced")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "synthetic-external-target" {
		t.Fatal("external race target changed")
	}
}
