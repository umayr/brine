package targetregistry

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSharedReadersAndDoubleClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	if err := r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	_, lease, err := r.AcquireRead(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := r.GetContext(ctx, old.ID)
	if err != nil || got != old {
		t.Fatalf("Get under lease: %v", err)
	}
	list, err := r.ListContext(ctx)
	if err != nil || len(list) != 1 || list[0] != old {
		t.Fatalf("List under lease: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := lease.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	type child struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
	}
	var children []child
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessHelper$")
		cmd.Env = append(os.Environ(), "BRINE_LEASE_HELPER="+dir)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{cmd, in})
		line, err := bufio.NewReader(out).ReadString('\n')
		if err != nil || line != "locked\n" {
			t.Fatalf("reader %d: %q %v", i, line, err)
		}
	}
	defer func() {
		for _, c := range children {
			if c.cmd.ProcessState == nil {
				c.cmd.Process.Kill()
				c.cmd.Wait()
			}
		}
	}()
	next := old
	next.Revision++
	next.Host = "next.example"
	done := make(chan error, 1)
	go func() { done <- r.Update(old, next, ReviewedUpdate{old, next}) }()
	blocked := func() {
		t.Helper()
		select {
		case e := <-done:
			t.Fatalf("writer before last reader closed: %v", e)
		case <-time.After(80 * time.Millisecond):
		}
	}
	blocked()
	children[0].stdin.Write([]byte("\n"))
	children[0].stdin.Close()
	if err := children[0].cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	blocked()
	children[1].stdin.Write([]byte("\n"))
	children[1].stdin.Close()
	if err := children[1].cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer stuck")
	}
	if err := r.Update(old, next, ReviewedUpdate{old, next}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
}
