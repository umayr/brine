package secretbroker

import (
	"context"
	"github.com/umayr/brine/internal/secrets"
	"testing"
	"time"
)

type acquireFunc func(context.Context, secrets.Selector) (secrets.Value, error)

func (f acquireFunc) Acquire(ctx context.Context, s secrets.Selector) (secrets.Value, error) {
	return f(ctx, s)
}
func TestShutdownCancelsAcquisitionAndClearsCopies(t *testing.T) {
	for _, reason := range []string{"shutdown", "caller"} {
		t.Run(reason, func(t *testing.T) {
			_, m, _, i, _ := reviewFixture(t)
			value, _ := secrets.NewValue([]byte("synthetic-shutdown-value"))
			entered := make(chan struct{})
			m.acquire = acquireFunc(func(ctx context.Context, _ secrets.Selector) (secrets.Value, error) {
				close(entered)
				<-ctx.Done()
				return value, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := m.Prepare(ctx, i, []string{"TOKEN"}); done <- err }()
			<-entered
			if reason == "shutdown" {
				m.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled preparation survived")
				}
			case <-time.After(time.Second):
				t.Fatal("acquisition outlived shutdown/context")
			}
			if len(value.Bytes()) != 0 {
				t.Fatal("acquired source copy not released")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.bytes != 0 || len(m.pending) != 0 || m.reserved != 0 {
				t.Fatal("cancelled preparation retained capacity/values")
			}
		})
	}
}
func TestShutdownWaitsForDispatchAndClearsDelivery(t *testing.T) {
	_, m, _, i, _ := reviewFixture(t)
	p, err := m.Prepare(context.Background(), i, []string{"TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var bytes []byte
	go func() {
		done <- m.Dispatch(context.Background(), p.ID, i, func(_ Preparation, d *Delivery) error {
			d.Each(func(_ string, b []byte) error { bytes = b; return nil })
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("shutdown bypassed dispatch fence")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown stuck")
	}
	for _, b := range bytes {
		if b != 0 {
			t.Fatal("delivery remained readable after dispatch/shutdown")
		}
	}
}
