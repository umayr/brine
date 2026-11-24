package remoteui

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/store"
	"testing"
	"time"
)

type blockedPollStore struct {
	store.StackStore
	rows    []*store.Stack
	entered chan string
	release chan struct{}
}

func (s *blockedPollStore) List() ([]*store.Stack, error) { return s.rows, nil }
func (s *blockedPollStore) Get(name string) (*store.Stack, error) {
	s.entered <- name
	<-s.release
	return nil, errors.New("synthetic unavailable")
}

func TestGitPollIndependentProgressBoundAndCancellation(t *testing.T) {
	st := &blockedPollStore{entered: make(chan string, 8), release: make(chan struct{})}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		st.rows = append(st.rows, &store.Stack{Name: name, RemoteGit: &store.RemoteGitPolicy{Enabled: true, Phase: "ready"}})
	}
	h := &Deployment{stacks: st}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	released := false
	defer func() {
		if !released {
			close(st.release)
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); h.pollGit(ctx) }()
	seen := map[string]bool{}
	for len(seen) < 4 {
		select {
		case name := <-st.entered:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("independent poll blocked behind another stack")
		}
	}
	select {
	case name := <-st.entered:
		t.Fatalf("exceeded four concurrent polls: %s", name)
	default:
	}
	cancel()
	close(st.release)
	released = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled poll did not join workers")
	}
	// Cancellation can race admission of a queued goroutine, but its context
	// must prevent any remote work and all admission entries must be released.
	if len(h.gitAdmission.active) != 0 {
		t.Fatal("admission leaked after cancellation")
	}
}
