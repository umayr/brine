package server

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/umayr/brine/internal/dill"
	gitpkg "github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/webhooks"
)

type webhookObservedOperationKey struct{}
type webhookLifecycleObserver struct {
	op       *webhooks.Operation
	ctx      context.Context
	started  bool
	finished bool
	result   webhooks.TriggerResult
}

func (o *webhookLifecycleObserver) Started() error {
	if o.started {
		return nil
	}
	if e := o.op.Started(o.ctx); e != nil {
		return e
	}
	o.started = true
	return nil
}
func (o *webhookLifecycleObserver) Finished(err error) { o.finish(err, nil) }
func (o *webhookLifecycleObserver) finish(err error, changed *bool) {
	kind, outcome, effects, reason := "operation.completed", "success", "unknown", ""
	if err != nil {
		if o.started {
			kind, outcome, reason = "operation.outcome_unknown", "unknown", "execution_unconfirmed"
		} else {
			kind, outcome, effects, reason = "operation.rejected", "unavailable", "none", "admission_denied"
		}
		var reported *dill.OperationError
		if errors.As(err, &reported) && reported.Invocation.Receipt != nil {
			kind = "operation.completed"
			outcome = reported.Invocation.Receipt.Outcome
			effects = reported.Invocation.Receipt.Effects
			reason = "reported_operation_result"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var e error
	if changed != nil && err == nil {
		e = o.op.FinishWithChanges(ctx, kind, outcome, effects, reason, *changed)
	} else {
		e = o.op.Finish(ctx, kind, outcome, effects, reason)
	}
	o.finished = true
	status := "completed"
	if kind == "operation.rejected" {
		status = "rejected"
	}
	if kind == "operation.outcome_unknown" {
		status = "unknown"
	}
	o.result = webhooks.TriggerResult{Status: status, Outcome: outcome, Reason: reason, OperationID: o.op.ID()}
	if e != nil {
		o.result.Status = "unknown"
		o.result.Outcome = "unknown"
		o.result.Reason = "operation_history_unavailable"
		log.Print("Webhook operation outcome could not be journaled; inspect the exact request and target before retrying")
	}
}

type webhookPullObserver struct{ *webhookLifecycleObserver }

func (o *webhookPullObserver) Finished(changed bool, err error) { o.finish(err, &changed) }

func (s *Server) newWebhookObservation(ctx context.Context, path, action string, repo bool) (*webhookLifecycleObserver, error) {
	if ctx.Value(webhookObservedOperationKey{}) != nil {
		return nil, nil
	}
	if s.store == nil || s.webhooks == nil {
		return nil, webhooks.ErrInvalid
	}
	stacks, e := s.store.List()
	if e != nil {
		return nil, webhooks.ErrInvalid
	}
	d := &webhookDispatcher{s: s}
	targetID := d.localTargetID()
	if targetID == "" {
		return nil, webhooks.ErrInvalid
	}
	for _, st := range stacks {
		matches := st.PklPath == path
		if repo {
			if st.Git == nil {
				continue
			}
			dir, e := s.store.RepoDir(st.Name)
			if e != nil {
				return nil, webhooks.ErrInvalid
			}
			matches = dir == path
		}
		if !matches {
			continue
		}
		sid := webhookStackID(st)
		if sid == "" {
			return nil, webhooks.ErrInvalid
		}
		source := "workspace"
		if st.Git != nil {
			source = "git"
		}
		target := webhooks.Resource{ID: targetID, Name: "Local / " + s.agent.Dill().Engine, Kind: "local"}
		if repo && st.RemoteGit != nil && (st.RemoteGit.Enabled || st.RemoteGit.Request != "") {
			target = webhooks.Resource{ID: "remote_" + st.RemoteGit.Target, Name: st.RemoteGit.Host, Kind: "remote"}
		}
		op, e := s.webhooks.BeginOperation(ctx, webhooks.Event{ID: webhooks.NewOperationID(), Stack: webhooks.Resource{ID: sid, Name: st.Name, Kind: "stack"}, Source: webhooks.Resource{Kind: source}, Target: target, Action: action})
		if e != nil {
			return nil, e
		}
		return &webhookLifecycleObserver{op: op, ctx: ctx}, nil
	}
	return nil, webhooks.ErrNotFound
}

// Hooks journal existing execution paths; they never substitute an executor,
// identity or authorization gate. Configure once before HTTP/background work.
func (s *Server) configureWebhookProducers() {
	if s.web != nil {
		s.web.SetBrokerExecutionObserver(s.newBrokerExecutionObservation)
	}
	if s.remoteDeploy != nil {
		s.remoteDeploy.SetExecutionObserver(s.newRemoteExecutionObservation)
	}
	if s.agent != nil && s.agent.Dill() != nil {
		s.agent.Dill().ObserveLifecycle = func(ctx context.Context, path, action string) (dill.LifecycleObserver, error) {
			observer, e := s.newWebhookObservation(ctx, path, action, false)
			if observer == nil {
				return nil, e
			}
			return observer, nil
		}
	}
	if s.git != nil {
		s.git.ObservePull = func(ctx context.Context, repo string) (gitpkg.PullObserver, error) {
			observer, e := s.newWebhookObservation(ctx, repo, "git-sync", true)
			if observer == nil {
				return nil, e
			}
			return &webhookPullObserver{observer}, nil
		}
	}
}
