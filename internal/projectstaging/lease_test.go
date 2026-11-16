package projectstaging

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
)

type leaseClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *leaseClient) Status(context.Context) (dillserve.Status, error) {
	return dillserve.Status{}, nil
}
func (c *leaseClient) Health(context.Context) (dillserve.Health, error) {
	return dillserve.Health{Capabilities: []string{"snapshot-staging"}}, nil
}
func (c *leaseClient) Stage(context.Context, []byte) (string, error) {
	close(c.entered)
	<-c.release
	return "staged", nil
}
func (c *leaseClient) Close() {}

// The trusted caller, not Sender, enforces this writer-excluding lease.
func TestCallerLeaseCoversSubmission(t *testing.T) {
	s, current, _ := sshFixture(t)
	var lock sync.RWMutex
	lock.RLock()
	id, token := approved(t, s)
	lock.RUnlock()
	c := &leaseClient{entered: make(chan struct{}), release: make(chan struct{})}
	s.openSSH = func(context.Context, dillserve.PinnedSSHOptions) (stagingClient, error) { return c, nil }
	result := make(chan error, 1)
	go func() {
		lock.RLock()
		defer lock.RUnlock()
		_, e := s.Stage(context.Background(), "session", id, token)
		result <- e
	}()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("submission not reached")
	}
	attempted := make(chan struct{})
	changed := make(chan struct{})
	go func() { close(attempted); lock.Lock(); current.Stack = "new"; lock.Unlock(); close(changed) }()
	<-attempted
	select {
	case <-changed:
		t.Fatal("writer passed held lease")
	default:
	}
	close(c.release)
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("writer blocked after stage")
	}
}
