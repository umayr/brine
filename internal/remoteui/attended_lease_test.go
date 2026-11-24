package remoteui

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRestoreOperatorModeDeniesBeforeAuthorizationOrArchive(t *testing.T) {
	checks := 0
	h := &Deployment{registry: &targetregistry.Registry{}, history: &attendedHistory{operator: &operatorDeployAuthority{}, restoreGate: func(context.Context, string, projectreview.Current, snapshothistory.ObjectID) error {
		checks++
		return nil
	}}}
	ctx := context.Background()
	locator := snapshothistory.ObjectID{1}
	if p, err := h.prepareRestoreReviewFromLocator(ctx, "session", "target", "stack", locator); p.ID != "" || !errors.Is(err, errRestoreReview) {
		t.Fatal("operator mode reached locator restore")
	}
	if p, err := h.prepareRestoreReview(ctx, "session", projectreview.Current{}, attendedBinding{}, snapshothistory.OpenRequest{ObjectID: locator}); p.ID != "" || !errors.Is(err, errRestoreReview) || checks != 0 {
		t.Fatal("operator mode reached bound restore or its gate")
	}
}

func TestRestoreCloseFailureDiscardsUnpublishedReview(t *testing.T) {
	ctx := context.Background()
	s, err := projectreview.New(1, 1<<20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current := projectreview.Current{Stack: "synthetic", Target: projectreview.Target{Kind: projectreview.HTTPS, Identity: "generated", Endpoint: "generated", Socket: "127.0.0.1:1"}}
	body := []byte("generated")
	intent := projectreview.Intent{Current: current, Snapshot: projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: body, SHA256: sha256.Sum256(body)}}}}
	p, err := s.Create(ctx, "session", intent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := finishRestoreLease(s, "session", p, nil, func() error { return targetregistry.ErrCleanup })
	if got.ID != "" || !errors.Is(err, errRestoreReview) || !errors.Is(err, targetregistry.ErrCleanup) {
		t.Fatalf("cleanup failure did not suppress review: %v", err)
	}
	if _, err := s.Confirm(ctx, "session", p.ID, current); err == nil {
		t.Fatal("unpublished review survived cleanup error")
	}
	if _, err := s.Create(ctx, "session", intent); err != nil {
		t.Fatalf("discard did not release review capacity: %v", err)
	}
}
