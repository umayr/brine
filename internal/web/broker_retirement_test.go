package web

import (
	"context"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/secretbroker"
	"strings"
	"testing"
	"time"
)

func seedWorkflowRetirement(t *testing.T, s *brokerapp.Service, scope secretbroker.Scope) {
	t.Helper()
	id := strings.Repeat("d", 32)
	digest := brokerwire.Digest([]byte("prior-source"))
	i := secretbroker.ReviewIdentity{Scope: scope, BindingRevision: 1, PolicyRevision: s.Revision(), SourceDigest: digest, TargetDigest: brokerwire.Digest([]byte("target")), SessionDigest: brokerwire.Digest([]byte("prior-session"))}
	ref := brokerwire.Reference{Scope: brokerwire.Scope{Registration: scope.RegistrationID, Stack: "demo", Engine: "docker", BindingRevision: 1, PolicyRevision: i.PolicyRevision, SourceDigest: digest, TargetDigest: i.TargetDigest, EndpointDigest: brokerwire.Digest([]byte("endpoint")), ConfigDigest: brokerwire.Digest([]byte("prior-config")), Generation: id}, Handle: id}
	intent := secretbroker.DeploymentIntent{ID: id, Identity: i, Reference: ref, StageID: id, JournalID: strings.Repeat("c", 32), SelectionDigest: brokerwire.Digest([]byte("selection")), Mode: "create"}
	if err := s.State.BeginDeployment(intent); err != nil {
		t.Fatal(err)
	}
	if err := s.State.AppendDeploymentEvent(scope, id, 0, secretbroker.DeploymentEvent{Sequence: 1, State: "succeeded", Effects: "complete", OperationID: "op_" + id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(scope, 1, "admin", nil); err != nil {
		t.Fatal(err)
	}
}
func (t *workflowTarget) Retire(_ context.Context, _ string, _ string, r brokerwire.Retirement) (brokerwire.Review, error) {
	if !t.retirement || !r.Valid() || len(r.Previous) != 1 || r.Previous[0].Handle != strings.Repeat("d", 32) {
		t.t.Fatal("non-authoritative retirement predecessor")
	}
	return brokerwire.Review{Scope: r.Scope, Handle: strings.Repeat("e", 32), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
