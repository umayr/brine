//go:build linux || darwin

package projectsnapshot

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func limits() Limits {
	return Limits{MaxFiles: 20, MaxEntries: 30, MaxDepth: 5, MaxFileBytes: 100, MaxTotalBytes: 200}
}
func put(t *testing.T, root, name, data string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestSnapshotDeterministic(t *testing.T) {
	root := t.TempDir()
	put(t, root, "main.pkl", "main")
	put(t, root, "modules/nested.pkl", "nested")
	put(t, root, "PklProject.deps.json", "lock")
	put(t, root, ".git/secret", "ignored")
	a, err := Build(root, "main.pkl", limits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(root, "main.pkl", limits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("snapshots differ")
	}
	var paths []string
	for _, f := range a.Files {
		paths = append(paths, f.Path)
		if f.SHA256 != sha256.Sum256(f.Content) {
			t.Fatal("incorrect hash")
		}
	}
	if !reflect.DeepEqual(paths, []string{"PklProject.deps.json", "main.pkl", "modules/nested.pkl"}) {
		t.Fatal(paths)
	}
}
func TestInvalidEntrypoints(t *testing.T) {
	root := t.TempDir()
	put(t, root, "main.pkl", "ok")
	for _, p := range []string{"", "/main.pkl", "../main.pkl", "a/../main.pkl", "./main.pkl", "a//b", "a/", `C:\x`, "C:/x", ".git/x", "missing"} {
		t.Run(p, func(t *testing.T) {
			if _, err := Build(root, p, limits()); err == nil {
				t.Fatal("accepted invalid entrypoint")
			}
		})
	}
}
func TestLimits(t *testing.T) {
	root := t.TempDir()
	put(t, root, "main.pkl", "1234")
	put(t, root, "dir/n.pkl", "5678")
	cases := map[string]func(*Limits){"files": func(l *Limits) { l.MaxFiles = 1 }, "entries": func(l *Limits) { l.MaxEntries = 2 }, "depth": func(l *Limits) { l.MaxDepth = 1 }, "file bytes": func(l *Limits) { l.MaxFileBytes = 3 }, "total bytes": func(l *Limits) { l.MaxTotalBytes = 7 }, "zero": func(l *Limits) { l.MaxFiles = 0 }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			l := limits()
			change(&l)
			if _, err := Build(root, "main.pkl", l); err == nil {
				t.Fatal("limit not enforced")
			}
		})
	}
	l := Limits{MaxFiles: 2, MaxEntries: 3, MaxDepth: 2, MaxFileBytes: 4, MaxTotalBytes: 8}
	if _, err := Build(root, "main.pkl", l); err != nil {
		t.Fatal(err)
	}
}
func TestRejectSpecialAndSymlink(t *testing.T) {
	for _, kind := range []string{"file symlink", "dir symlink", "fifo", "root symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "main.pkl", "ok")
			target := filepath.Join(root, "bad")
			switch kind {
			case "file symlink":
				if err := os.Symlink("main.pkl", target); err != nil {
					t.Fatal(err)
				}
			case "dir symlink":
				if err := os.Symlink(".", target); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "root symlink":
				target = filepath.Join(t.TempDir(), "root")
				if err := os.Symlink(root, target); err != nil {
					t.Fatal(err)
				}
				root = target
			}
			if _, err := Build(root, "main.pkl", limits()); err == nil {
				t.Fatal("accepted unsafe entry")
			}
		})
	}
}
func TestDetectReplacementBetweenListingAndOpen(t *testing.T) {
	root := t.TempDir()
	put(t, root, "main.pkl", "old")
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	var listed unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), "main.pkl", &listed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	put(t, root, "replacement", "new")
	if err := os.Rename(filepath.Join(root, "replacement"), filepath.Join(root, "main.pkl")); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "main.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := walker{limits: limits()}
	if err := w.capture(dir, f, "main.pkl", "main.pkl", 0, &listed); err == nil {
		t.Fatal("replacement not detected")
	}
}
