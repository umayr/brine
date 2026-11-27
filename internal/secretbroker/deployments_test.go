package secretbroker

import (
	"context"
	"github.com/umayr/brine/internal/brokerwire"
	"strings"
	"testing"
)

func TestDeploymentIntentImmutableUnknownAndRevisionMetadata(t *testing.T) {
	s, m, _, i, path := reviewFixture(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := brokerwire.Reference{Handle: strings.Repeat("2", 32), Scope: brokerwire.Scope{Registration: i.Scope.RegistrationID, Stack: i.Scope.StackName, Engine: "docker", TargetDigest: i.TargetDigest, SourceDigest: i.SourceDigest, ConfigDigest: digest, EndpointDigest: digest, PolicyRevision: i.PolicyRevision, Generation: strings.Repeat("3", 32), BindingRevision: uint64(i.BindingRevision)}}
	intent := DeploymentIntent{ID: strings.Repeat("1", 32), Identity: i, Reference: ref, StageID: strings.Repeat("4", 32), JournalID: strings.Repeat("5", 32), SelectionDigest: digest, Mode: "create"}
	wantError(t, s.BeginDeployment(intent), nil)
	wantError(t, s.BeginDeployment(intent), ErrConflict)
	called := false
	wantError(t, m.WithCurrent(context.Background(), i, func() error { called = true; return nil }), nil)
	if !called {
		t.Fatal("not dispatched")
	}
	wantError(t, s.AppendDeploymentEvent(i.Scope, intent.ID, 0, DeploymentEvent{Sequence: 1, State: "unknown", Effects: "unknown"}), nil)
	s.Close()
	reopened, err := Open(path)
	wantError(t, err, nil)
	defer reopened.Close()
	records, err := reopened.Deployments(i.Scope)
	wantError(t, err, nil)
	if len(records) != 1 || records[0].Intent.Reference != ref || records[0].Events[1].State != "unknown" {
		t.Fatal("lost immutable scope or unknown")
	}
	wantError(t, reopened.AppendDeploymentEvent(i.Scope, intent.ID, 1, DeploymentEvent{Sequence: 2, State: "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("6", 32)}), nil)
	wantError(t, reopened.AppendDeploymentEvent(i.Scope, intent.ID, 2, DeploymentEvent{Sequence: 3, State: "unknown", Effects: "unknown"}), ErrConflict)
}
