package brokerapp

import (
	"context"
	"database/sql"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"strings"
	"testing"
	"time"
)

type controllerAcquire struct{}

func (controllerAcquire) Acquire(context.Context, secrets.Selector) (secrets.Value, error) {
	return secrets.NewValue([]byte("synthetic-static-password"))
}

type controllerTarget struct {
	t            *testing.T
	state        *secretbroker.Store
	identity     secretbroker.ReviewIdentity
	scope        brokerwire.Scope
	unknown      bool
	writes       int
	status       string
	databasePath string
	intentExists func(string) bool
}

func (t *controllerTarget) Info(context.Context) (TargetInfo, error) {
	return TargetInfo{strings.Repeat("a", 32), t.identity.TargetDigest, brokerwire.Digest([]byte("endpoint"))}, nil
}
func (t *controllerTarget) Requirements(context.Context, string, string, string, string) (brokerwire.Requirements, error) {
	return brokerwire.Requirements{Contract: brokerwire.Version, Stack: "demo", Engine: "docker", ConfigDigest: brokerwire.Digest([]byte("config")), Names: []string{"password"}, Grants: []brokerwire.Grant{{Service: "web", Name: "password", Target: "/run/secrets/password", Mode: 0400}}}, nil
}
func (t *controllerTarget) Prepare(_ context.Context, _ string, _ string, e brokerwire.Envelope) (brokerwire.Review, error) {
	t.scope = e.Scope
	if len(e.Items) != 1 || string(e.Items[0].Bytes()) != "synthetic-static-password" {
		t.t.Fatal("not frozen exact bytes")
	}
	return brokerwire.Review{Scope: e.Scope, Handle: strings.Repeat("b", 32), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (t *controllerTarget) Review(_ context.Context, _ string, _ string, r brokerwire.Reference) (*dill.BrokerReview, string, error) {
	d := brokerwire.Digest([]byte("selection"))
	return &dill.BrokerReview{Reference: r, Preflight: &dill.PreflightResult{SelectionDigest: &d}, Plan: &dill.BrokerPlan{Changes: []dill.PlanChange{{Service: "web", Kind: "create"}}}}, "create", nil
}
func (t *controllerTarget) Apply(_ context.Context, _ string, _ string, id string, _ string, _ string, r brokerwire.Reference) (Outcome, error) {
	t.writes++
	if t.intentExists == nil || !t.intentExists(id) {
		t.t.Fatal("target dispatch preceded committed application intent")
	}
	if t.unknown {
		return Outcome{}, ErrUnavailable
	}
	return Outcome{id, "demo", "succeeded", "complete", "op_" + id}, nil
}
func (t *controllerTarget) Status(_ context.Context, _ string, id string, _ brokerwire.Reference) (Outcome, error) {
	if t.status == "unknown" {
		return Outcome{id, "demo", "unknown", "unknown", ""}, nil
	}
	return Outcome{id, "demo", "succeeded", "complete", "op_" + id}, nil
}
func controllerFixture(t *testing.T) (*Controller, *controllerTarget, secretbroker.ReviewIdentity) {
	data, path, scope := serviceFixture(t)
	s, err := Open(data, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	_, err = s.Save(scope, 0, "admin", []secretbroker.Binding{{Name: "password", Kind: secretbroker.ProviderLocal, ConnectionID: "files", LocalAlias: "password"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Manager.Close()
	s.Manager, err = secretbroker.NewManager(s.State, controllerAcquire{}, func(_ context.Context, i secretbroker.ReviewIdentity, b []secretbroker.Binding) error {
		if i.PolicyRevision != s.Revision() {
			return ErrUnavailable
		}
		return s.Allowed(i.Scope, b)
	})
	if err != nil {
		t.Fatal(err)
	}
	d := brokerwire.Digest([]byte("source"))
	i := secretbroker.ReviewIdentity{Scope: scope, SourceDigest: d, TargetDigest: brokerwire.Digest([]byte("target")), PolicyRevision: s.Revision(), SessionDigest: brokerwire.Digest([]byte("session")), BindingRevision: 1}
	c, err := NewController(s, func(ctx context.Context, current secretbroker.ReviewIdentity) (func(), error) {
		if current != i || ctx.Err() != nil {
			return nil, ErrUnavailable
		}
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+statePath(data)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return c, &controllerTarget{t: t, state: s.State, identity: i, databasePath: statePath(data), intentExists: func(id string) bool {
		var count int
		return reader.QueryRow("SELECT count(*) FROM broker_deployments WHERE id=?", id).Scan(&count) == nil && count == 1
	}}, i
}
func TestControllerUnknownIsDurableAndNeverResubmitted(t *testing.T) {
	c, target, i := controllerFixture(t)
	target.unknown = true
	review, err := c.Prepare(context.Background(), i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), target)
	if err != nil {
		t.Fatal(err)
	}
	record, err := c.Submit(context.Background(), review.ID, i, target)
	if err == nil || target.writes != 1 {
		t.Fatal("unknown apply succeeded or was repeated")
	}
	rows, err := c.service.State.Deployments(i.Scope)
	if err != nil || len(rows) != 1 || rows[0].Events[len(rows[0].Events)-1].State != "unknown" {
		t.Fatal("unknown not retained")
	}
	if _, err = c.Submit(context.Background(), review.ID, i, target); err == nil || target.writes != 1 {
		t.Fatal("replayed consumed review")
	}
	if _, err = c.Prepare(context.Background(), i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), target); err == nil {
		t.Fatal("unknown request allowed another preparation")
	}
	target.status = "unknown"
	for n := 0; n < 40; n++ {
		if _, err = c.Reconcile(context.Background(), record, target, func(context.Context, secretbroker.Scope) error { return nil }); err != nil {
			t.Fatal("unchanged polling consumed event capacity", err)
		}
	}
	target.status = ""
	event, err := c.Reconcile(context.Background(), record, target, func(context.Context, secretbroker.Scope) error { return nil })
	if err != nil || event.State != "succeeded" || target.writes != 1 {
		t.Fatal("lookup resubmitted or failed reconciliation")
	}
}
func TestControllerBindingChangeCannotDispatchReviewedHandle(t *testing.T) {
	c, target, i := controllerFixture(t)
	review, err := c.Prepare(context.Background(), i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.service.State.Save(i.Scope, 1, "admin", nil); err != nil {
		t.Fatal(err)
	}
	record, err := c.Submit(context.Background(), review.ID, i, target)
	if err == nil || target.writes != 0 || record.Events[len(record.Events)-1].State != "not-admitted" {
		t.Fatal("stale bindings dispatched")
	}
}
