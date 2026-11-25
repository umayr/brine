package snapshothistory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type restartAdmissionApproval struct{}

func (restartAdmissionApproval) AuthorizeSnapshotHistoryRestart(context.Context, []byte) error {
	return nil
}

func TestRestartDiscoveryBusyDoesNotQueue(t *testing.T) {
	s := new(Store)
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := s.DiscoverRestartMetadata(context.Background(), RestartDiscoveryRequest{ApprovedTargetScope: []byte("test-scope"), Authorizer: restartAdmissionApproval{}, Limit: 1})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unexpected busy result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery waited for archive lock")
	}
}
