package remoteui

import (
	"context"
	"github.com/umayr/brine/internal/store"
	"testing"
	"time"
)

func waitQueued(t *testing.T, g *deploymentAdmission) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		n := len(g.queue)
		g.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(until) {
			t.Fatal("poll never queued")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueuedPollCannotBeBypassedByManualWork(t *testing.T) {
	st := &blockedPollStore{entered: make(chan string, 1), release: make(chan struct{}), rows: []*store.Stack{{Name: "recover", RemoteGit: &store.RemoteGitPolicy{Request: "pending"}}}}
	h := &Deployment{stacks: st}
	var release []func()
	for _, name := range []string{"a", "b", "c", "d"} {
		r, ok := h.gitAdmission.try(name)
		if !ok {
			t.Fatal("setup")
		}
		release = append(release, r)
		defer r()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); h.pollGit(ctx) }()
	defer close(st.release)
	waitQueued(t, &h.gitAdmission)
	release[0]()
	if r, ok := h.gitAdmission.try("manual-new"); ok {
		r()
		t.Fatal("manual bypassed queued pending recovery")
	}
	select {
	case name := <-st.entered:
		if name != "recover" {
			t.Fatal(name)
		}
	case <-time.After(time.Second):
		t.Fatal("pending recovery starved")
	}
	cancel()
	// release the blocked synthetic Get independently of remote/network state.
	st.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll not joined")
	}
}

func TestQueuedAdmissionCancellationReleasesReservation(t *testing.T) {
	var g deploymentAdmission
	release, _ := g.try("same")
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		r, ok := g.acquire(ctx, "same")
		if ok {
			r()
		}
		done <- ok
	}()
	waitQueued(t, &g)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled waiter admitted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter blocked")
	}
	g.mu.Lock()
	n := len(g.queue)
	g.mu.Unlock()
	if n != 0 {
		t.Fatal("queue leaked")
	}
	release()
	r, ok := g.try("same")
	if !ok {
		t.Fatal("capacity leaked")
	}
	r()
}
