//go:build unix

package dill

import (
	"context"
	"errors"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/target"
)

func TestAlphaSecretWorkflowCannotActivateOrApply(t *testing.T) {
	r := New("/must-not-run")
	r.Profile = target.Alpha
	// Deliberately unopened state: any state access would fail/panic. The profile
	// boundary must reject before consuming a saved preview or activating a draft.
	s := &SecretRunner{Runner: r, State: &secretbindings.Store{}, Coordinator: &r.Coordinator, Engine: "docker", Socket: "/synthetic/selected.sock", Policy: "/synthetic/policy", Target: "configured-policy-name"}
	ctx := context.Background()
	for _, f := range []func() error{
		func() error { return s.ConfirmAndApplyLocked(ctx, "id", "demo", "/synthetic/recipe", "token") },
		func() error { return s.ApplyActive(ctx, "id", "demo", "/synthetic/recipe") },
		func() error { _, _, err := s.PreviewLocked(ctx, "id", "demo", "/synthetic/recipe"); return err },
	} {
		if err := f(); !errors.Is(err, target.ErrNegotiationUnavailable) {
			t.Fatalf("secret profile bypass: %v", err)
		}
	}
}
