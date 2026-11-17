package revealaudit

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReservedCapacityConcurrent(t *testing.T) {
	a, _ := setup(t)
	a.limit = 1600
	var wg sync.WaitGroup
	results := make(chan Intent, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, e := a.BeginRevealAudit(ctx, "admin", "target", "container")
			if e == nil {
				results <- in
			}
		}()
	}
	wg.Wait()
	close(results)
	n := 0
	for in := range results {
		n++
		if e := a.Finish(ctx, in.Correlation, Succeeded); e != nil {
			t.Fatalf("admitted intent lacked outcome capacity: %v", e)
		}
	}
	if n == 0 || n == 20 {
		t.Fatalf("unexpected admitted count %d", n)
	}
}
func TestIncompleteInitializationIsNotRepaired(t *testing.T) {
	for _, files := range [][]string{{"lock"}, {"lock", "head"}} {
		t.Run(fmt.Sprint(files), func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			for _, name := range files {
				if e := os.WriteFile(filepath.Join(root, name), nil, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := Open(ctx, root); e == nil {
				t.Fatal("incomplete init accepted")
			}
			if _, e := Initialize(ctx, root); e == nil {
				t.Fatal("incomplete init overwritten")
			}
		})
	}
}
func TestFinishFaultStates(t *testing.T) {
	for _, stage := range []string{"head-write", "head-fsync", "write", "fsync", "close", "dirsync", "dirsync-final"} {
		t.Run(stage, func(t *testing.T) {
			a, root := setup(t)
			in, e := a.BeginRevealAudit(ctx, "admin", "target", "container")
			if e != nil {
				t.Fatal(e)
			}
			a.fault = func(s string) error {
				if s == stage {
					return os.ErrInvalid
				}
				return nil
			}
			if e = a.Finish(ctx, in.Correlation, Succeeded); e == nil {
				t.Fatal("accepted injected failure")
			}
			b, e := Open(ctx, root)
			if stage == "head-fsync" || stage == "write" {
				if e == nil {
					t.Fatal("accepted mismatched head")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			es, e := b.Entries(ctx)
			if e != nil || len(es) != 1 {
				t.Fatalf("%v %v", es, e)
			}
			want := Outcome("")
			if stage == "fsync" || stage == "close" || stage == "dirsync-final" {
				want = Succeeded
			}
			if es[0].Outcome != want {
				t.Fatalf("outcome %q want %q", es[0].Outcome, want)
			}
		})
	}
}

func TestTruncateToPreviousRecordBoundary(t *testing.T) {
	a, root := setup(t)
	in, e := a.BeginRevealAudit(ctx, "admin", "target", "container")
	if e != nil {
		t.Fatal(e)
	}
	journal := filepath.Join(root, "journal")
	previous, e := os.ReadFile(journal)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Finish(ctx, in.Correlation, Succeeded); e != nil {
		t.Fatal(e)
	}
	if e = os.Truncate(journal, int64(len(previous))); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(ctx, root); e == nil {
		t.Fatal("accepted truncation to previous complete record")
	}
}

func TestCanonicalMalformedState(t *testing.T) {
	a, root := setup(t)
	in, e := a.BeginRevealAudit(ctx, "admin", "target", "container")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Finish(ctx, in.Correlation, Succeeded); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, "journal")
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var last int
	for i := len(data) - 2; i >= 0; i-- {
		if data[i] == '\n' {
			last = i + 1
			break
		}
	}
	data = append(data, data[last:]...)
	sum := sha256.Sum256(data)
	if e = os.WriteFile(p, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "head"), []byte(fmt.Sprintf("%016x %x\n", len(data), sum)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(ctx, root); e == nil {
		t.Fatal("duplicate outcome accepted")
	}
}
