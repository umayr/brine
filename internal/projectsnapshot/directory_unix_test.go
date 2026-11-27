//go:build linux || darwin

package projectsnapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDirectoryPinsRootAndPreservesCaller(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"compose.pkl", "other.txt"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte(n), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	first, err := dir.Readdirnames(1)
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	limits := Limits{10, 20, 4, 1024, 4096}
	for n := 0; n < 2; n++ {
		snap, err := BuildDirectory(dir, "compose.pkl", limits)
		if err != nil || len(snap.Files) != 2 {
			t.Fatal("capture advanced or closed handle", err)
		}
	}
	rest, err := dir.Readdirnames(-1)
	if err != nil || len(rest) != 1 || rest[0] == first[0] {
		t.Fatal("caller directory cursor was changed", err)
	}
	if _, err = BuildDirectory(dir, "../escape", limits); err == nil {
		t.Fatal("entrypoint traversal allowed")
	}
	if _, err = BuildDirectory(nil, "compose.pkl", limits); err == nil {
		t.Fatal("nil directory accepted")
	}
}
