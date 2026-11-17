package revealaudit

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

func setup(t *testing.T) (*Audit, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := Initialize(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return a, root
}
func TestReopenAndFinish(t *testing.T) {
	a, root := setup(t)
	i, err := a.BeginRevealAudit(ctx, "admin", "target", "container")
	if err != nil {
		t.Fatal(err)
	}
	a, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Pending(ctx)
	if err != nil || len(p) != 1 || p[0] != i {
		t.Fatalf("%v %v", p, err)
	}
	if err = a.Finish(ctx, i.Correlation, Succeeded); err != nil {
		t.Fatal(err)
	}
	if err = a.Finish(ctx, i.Correlation, Failed); err == nil {
		t.Fatal("duplicate")
	}
	es, err := a.Entries(ctx)
	if err != nil || len(es) != 1 || es[0].Outcome != Succeeded {
		t.Fatalf("%v %v", es, err)
	}
}
func TestParallel(t *testing.T) {
	a, root := setup(t)
	b, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ch := make(chan Intent, 40)
	for k := 0; k < 40; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			x := a
			if k%2 == 0 {
				x = b
			}
			i, e := x.BeginRevealAudit(ctx, fmt.Sprint(k), "target", "container")
			if e != nil {
				t.Error(e)
				return
			}
			ch <- i
		}(k)
	}
	wg.Wait()
	close(ch)
	var is []Intent
	for i := range ch {
		is = append(is, i)
	}
	if len(is) != 40 {
		t.Fatal(len(is))
	}
	success := make(chan bool, 80)
	for _, i := range is {
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func(i Intent) { defer wg.Done(); success <- a.Finish(ctx, i.Correlation, Succeeded) == nil }(i)
		}
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 40 {
		t.Fatal(n)
	}
}
func TestCorruption(t *testing.T) {
	for _, mode := range []string{"delete", "truncate", "partial", "head", "missing-head"} {
		t.Run(mode, func(t *testing.T) {
			a, root := setup(t)
			if _, e := a.BeginRevealAudit(ctx, "admin", "target", "container"); e != nil {
				t.Fatal(e)
			}
			journal := filepath.Join(root, "journal")
			switch mode {
			case "delete":
				os.Remove(journal)
			case "truncate":
				os.Truncate(journal, 0)
			case "partial":
				os.WriteFile(journal, []byte("{"), 0600)
			case "head":
				os.WriteFile(filepath.Join(root, "head"), []byte("bad"), 0600)
			case "missing-head":
				os.Remove(filepath.Join(root, "head"))
			}
			if _, e := Open(ctx, root); e == nil {
				t.Fatal("accepted corruption")
			}
		})
	}
}
func TestFaults(t *testing.T) {
	for _, stage := range []string{"head-write", "head-fsync", "write", "fsync", "close", "dirsync", "dirsync-final"} {
		t.Run(stage, func(t *testing.T) {
			a, root := setup(t)
			a.fault = func(s string) error {
				if s == stage {
					return errors.New("injected")
				}
				return nil
			}
			if _, e := a.BeginRevealAudit(ctx, "admin", "target", "container"); e == nil {
				t.Fatal("accepted fault")
			}
			b, e := Open(ctx, root)
			switch stage {
			case "head-fsync", "write":
				if e == nil {
					t.Fatal("head mismatch accepted")
				}
			default:
				if e != nil {
					t.Fatal(e)
				}
				es, e := b.Entries(ctx)
				if e != nil {
					t.Fatal(e)
				}
				want := 0
				if stage == "fsync" || stage == "close" || stage == "dirsync-final" {
					want = 1
				}
				if len(es) != want {
					t.Fatalf("entries=%v", es)
				}
			}
		})
	}
}
func TestLockDeadline(t *testing.T) {
	a, root := setup(t)
	f, e := os.OpenFile(filepath.Join(root, "lock"), os.O_RDWR, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX); e != nil {
		t.Fatal(e)
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, e = a.BeginRevealAudit(c, "admin", "target", "container"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_UN); e != nil {
		t.Fatal(e)
	}
	// Give any accidentally detached writer time to acquire the released lock.
	time.Sleep(100 * time.Millisecond)
	entries, e := a.Entries(ctx)
	if e != nil || len(entries) != 0 {
		t.Fatalf("delayed mutation: %v, %v", entries, e)
	}
	data, e := os.ReadFile(filepath.Join(root, "journal"))
	if e != nil || len(data) != 0 {
		t.Fatalf("journal changed: %q, %v", data, e)
	}
}
func TestPermissions(t *testing.T) {
	_, root := setup(t)
	os.Chmod(root, 0755)
	if _, e := Open(ctx, root); e == nil {
		t.Fatal("public root")
	}
	os.Chmod(root, 0700)
	os.Chmod(filepath.Join(root, "journal"), 0644)
	if _, e := Open(ctx, root); e == nil {
		t.Fatal("public journal")
	}
}
