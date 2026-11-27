package brokerapp

import (
	"context"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/secretbroker"
	"strings"
	"testing"
	"time"
)

type retirementTarget struct {
	*controllerTarget
	request brokerwire.Retirement
	refuses bool
}

func (t *retirementTarget) Requirements(context.Context, string, string, string, string) (brokerwire.Requirements, error) {
	return brokerwire.Requirements{Contract: brokerwire.Version, Stack: "demo", Engine: "docker", ConfigDigest: brokerwire.Digest([]byte("empty-config")), Names: []string{}, Grants: []brokerwire.Grant{}}, nil
}
func (t *retirementTarget) Prepare(context.Context, string, string, brokerwire.Envelope) (brokerwire.Review, error) {
	t.t.Fatal("retirement acquired normal provider delivery")
	return brokerwire.Review{}, ErrUnavailable
}
func (t *retirementTarget) Retire(_ context.Context, _ string, _ string, r brokerwire.Retirement) (brokerwire.Review, error) {
	t.request = r
	if t.refuses {
		return brokerwire.Review{}, ErrUnavailable
	}
	return brokerwire.Review{Scope: r.Scope, Handle: strings.Repeat("e", 32), ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func TestControllerRetirement(t *testing.T) {
	for _, scenario := range []string{"unknown", "stale", "missing", "omitted", "bindings", "foreign", "policy", "session"} {
		t.Run(scenario, func(t *testing.T) {
			c, base, i := controllerFixture(t)
			ctx := context.Background()
			stage := strings.Repeat("c", 32)
			manifest := strings.TrimPrefix(i.SourceDigest, "sha256:")
			if scenario != "missing" {
				review, err := c.Prepare(ctx, i, stage, manifest, base)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.Submit(ctx, review.ID, i, base); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "bindings" {
				if _, err := c.service.Save(i.Scope, 1, "admin", nil); err != nil {
					t.Fatal(err)
				}
				i.BindingRevision = 2
			}
			c.guard = func(_ context.Context, identity secretbroker.ReviewIdentity) (func(), error) {
				if identity.SessionDigest != i.SessionDigest {
					return nil, ErrUnavailable
				}
				return func() {}, nil
			}
			target := &retirementTarget{controllerTarget: base, refuses: scenario == "omitted"}
			if scenario == "foreign" {
				i.TargetDigest = brokerwire.Digest([]byte("foreign"))
				base.identity = i
			}
			review, err := c.Prepare(ctx, i, stage, manifest, target)
			if scenario == "missing" || scenario == "bindings" || scenario == "omitted" || scenario == "foreign" {
				if err == nil {
					t.Fatal("unfounded retirement admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if review.Retirement == nil || len(review.Retirement.Previous) != 1 || review.Retirement.Previous[0].Handle != strings.Repeat("b", 32) || len(review.Revisions) != 0 {
				t.Fatal("predecessor not derived from immutable history")
			}
			if scenario == "stale" {
				if _, err = c.service.Save(i.Scope, 2, "admin", nil); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "session" {
				i.SessionDigest = brokerwire.Digest([]byte("revoked"))
			}
			if scenario == "policy" {
				if err = c.service.Manager.ChangePolicy(func() error { c.service.revision = brokerwire.Digest([]byte("changed")); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			base.unknown = true
			before := base.writes
			record, err := c.Submit(ctx, review.ID, i, target)
			if err == nil {
				t.Fatal("unknown/stale reported success")
			}
			if scenario != "unknown" {
				if base.writes != before {
					t.Fatal("revoked review dispatched")
				}
				return
			}
			if base.writes != before+1 || record.Intent.Retirement == nil {
				t.Fatal("missing immutable retirement intent")
			}
			rows, err := c.service.State.Deployments(i.Scope)
			if err != nil || rows[0].Intent.Retirement == nil {
				t.Fatal("retirement lost in history")
			}
			if _, err = c.Prepare(ctx, i, stage, manifest, target); err == nil {
				t.Fatal("unknown allowed another retirement")
			}
			if _, err = c.Submit(ctx, review.ID, i, target); err == nil || base.writes != before+1 {
				t.Fatal("retirement replayed")
			}
			base.status = "unknown"
			if _, err = c.Reconcile(ctx, record, target, func(context.Context, secretbroker.Scope) error { return nil }); err != nil {
				t.Fatal(err)
			}
			base.status = ""
			if _, err = c.Reconcile(ctx, record, target, func(context.Context, secretbroker.Scope) error { return nil }); err != nil || base.writes != before+1 {
				t.Fatal("status replayed retirement")
			}
		})
	}
}
