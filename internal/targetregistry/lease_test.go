package targetregistry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReadLeaseStreamAndCancellation(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	if err := r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got, lease, err := r.AcquireRead(ctx, old.ID)
	if err != nil || got != old {
		t.Fatalf("snapshot: %v %v", got, err)
	}
	cancel() // cancelling acquisition context must not release an established stream lease
	next := old
	next.Revision++
	next.Host = "new.example"
	done := make(chan error, 1)
	go func() { done <- r.Update(old, next, ReviewedUpdate{old, next}) }()
	select {
	case err := <-done:
		t.Fatalf("writer passed stream: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	wait, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	if err := r.UpdateContext(wait, old, next, ReviewedUpdate{old, next}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled writer: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
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
	fresh, l, err := r.AcquireRead(context.Background(), old.ID)
	if err != nil || fresh != next {
		t.Fatalf("new snapshot: %v %v", fresh, err)
	}
	l.Close()
}

func TestLeaseProcessHelper(t *testing.T) {
	dir := os.Getenv("BRINE_LEASE_HELPER")
	if dir == "" {
		return
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, l, err := r.AcquireRead(context.Background(), record().ID)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	bufio.NewReader(os.Stdin).ReadString('\n')
	l.Close()
}

func TestReadLeaseProcessCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	if err = r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseProcessHelper$")
	cmd.Env = append(os.Environ(), "BRINE_LEASE_HELPER="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper: %q %v", line, err)
	}
	next := old
	next.Revision++
	next.Host = "new.example"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err = r.UpdateContext(ctx, old, next, ReviewedUpdate{old, next}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writer did not wait: %v", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err = r.UpdateContext(ctx2, old, next, ReviewedUpdate{old, next}); err != nil {
		t.Fatalf("crash did not release: %v", err)
	}
}

func TestAcquireReadFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, l, err := r.AcquireRead(context.Background(), "missing"); err == nil || l != nil {
		t.Fatal("missing lock accepted")
	}
	old := record()
	if err := r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	if _, l, err := r.AcquireRead(context.Background(), "missing"); !errors.Is(err, ErrNotFound) || l != nil {
		t.Fatalf("missing record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "targets.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, l, err := r.AcquireRead(context.Background(), old.ID); err == nil || l != nil {
		t.Fatal("malformed data accepted")
	}
	if err := os.Chmod(filepath.Join(dir, ".lock"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, l, err := r.AcquireRead(context.Background(), old.ID); err == nil || l != nil {
		t.Fatal("malformed lock accepted")
	}
}
