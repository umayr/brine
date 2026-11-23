//go:build linux || darwin

package sqlitestore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryDirectoryAdmission(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TMPDIR", home)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := openHistoryDir(root)
	if err != nil {
		t.Skipf("no safe disposable anchor %s: %v", root, err)
	}
	f.Close()
	child := filepath.Join(root, "history")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	f, err = openHistoryDir(child)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	missing := filepath.Join(root, "absent")
	if f, err = openHistoryDir(missing); err == nil {
		f.Close()
		t.Fatal("accepted missing")
	}
	if _, err = os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("validator created missing directory")
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "nested"), child + "/.", "relative", "/"} {
		if f, err = openHistoryDir(path); err == nil {
			f.Close()
			t.Fatalf("accepted %q", path)
		}
	}
	if err = os.Chmod(child, 0750); err != nil {
		t.Fatal(err)
	}
	if f, err = openHistoryDir(child); err == nil {
		f.Close()
		t.Fatal("accepted non-private directory")
	}
	if err = os.Chmod(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	if f, err = openHistoryDir(child); err == nil {
		f.Close()
		t.Fatal("accepted sticky writable ancestor")
	}
}
