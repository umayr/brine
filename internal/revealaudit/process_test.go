package revealaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestProcessLock(t *testing.T) {
	if root := os.Getenv("REVEALAUDIT_TEST_ROOT"); root != "" {
		a, e := Open(ctx, root)
		if e != nil {
			t.Fatal(e)
		}
		for i := 0; i < 10; i++ {
			if _, e := a.BeginRevealAudit(ctx, "admin", "target", "container"); e != nil {
				t.Fatal(e)
			}
		}
		return
	}
	a, root := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLock$")
			cmd.Env = append(os.Environ(), "REVEALAUDIT_TEST_ROOT="+root)
			out, e := cmd.CombinedOutput()
			if e != nil {
				t.Errorf("%v %s", e, out)
			}
		}()
	}
	wg.Wait()
	es, e := a.Entries(ctx)
	if e != nil || len(es) != 40 {
		t.Fatalf("%d %v", len(es), e)
	}
}
func TestLinks(t *testing.T) {
	_, root := setup(t)
	j := filepath.Join(root, "journal")
	if e := os.Link(j, filepath.Join(root, "other")); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(ctx, root); e == nil {
		t.Fatal("hard link accepted")
	}
	root2 := t.TempDir()
	os.Chmod(root2, 0700)
	if e := os.Symlink(j, filepath.Join(root2, "lock")); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(ctx, root2); e == nil {
		t.Fatal("symlink accepted")
	}
}
