package brokerapp

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/secretbroker"
	"os"
	"strings"
	"testing"
)

type capturedExecution struct {
	meta             ExecutionMetadata
	start            func() error
	starts, finishes int
	status           Outcome
	err              error
	sent             bool
}

func (o *capturedExecution) Started() error {
	o.starts++
	if o.start != nil {
		return o.start()
	}
	return nil
}
func (o *capturedExecution) Finished(status Outcome, err error, sent bool) {
	o.finishes++
	o.status, o.err, o.sent = status, err, sent
}

type observedTarget struct {
	*controllerTarget
	mode string
}

func (t observedTarget) Apply(ctx context.Context, j, stage, id, selection, mode string, ref brokerwire.Reference) (Outcome, error) {
	out, err := t.controllerTarget.Apply(ctx, j, stage, id, selection, mode, ref)
	switch t.mode {
	case "partial":
		out.State, out.Effects = "failed", "partial"
	case "foreign":
		out.Stack = "other"
	case "none":
		out.Effects = "none"
	case "history-failure":
		path := t.databasePath
		if err := os.Rename(path, path+".retained"); err != nil {
			t.t.Fatal(err)
		}
		t.t.Cleanup(func() {
			if err := os.Rename(path+".retained", path); err != nil {
				t.t.Error(err)
			}
		})
	}
	return out, err
}
func TestBrokerObservationPreservesOutcomeAndDoesNotObserveRecovery(t *testing.T) {
	for _, mode := range []string{"success", "none", "partial", "unknown", "foreign", "history-failure"} {
		t.Run(mode, func(t *testing.T) {
			c, target, i := controllerFixture(t)
			target.unknown = mode == "unknown"
			var observations []*capturedExecution
			c.SetExecutionObserver(func(_ context.Context, meta ExecutionMetadata) (ExecutionObservation, error) {
				o := &capturedExecution{meta: meta}
				o.start = func() error {
					if target.writes != 0 {
						t.Fatal("observation started after target write")
					}
					if meta.Action == "deploy" && !target.intentExists(meta.Request) {
						t.Fatal("start preceded durable intent")
					}
					return nil
				}
				observations = append(observations, o)
				return o, nil
			})
			ctx := context.Background()
			review, err := c.Prepare(ctx, i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), target)
			if err != nil || len(observations) != 1 {
				t.Fatal(err, len(observations))
			}
			plan := observations[0]
			if plan.meta.Action != "plan" || plan.meta.Request != "" || plan.starts != 1 || plan.finishes != 1 || !plan.sent || plan.err != nil {
				t.Fatal("plan observation not bounded", plan)
			}
			record, err := c.Submit(ctx, review.ID, i, observedTarget{target, mode})
			success := mode == "success" || mode == "none"
			if (err == nil) != success || target.writes != 1 || len(observations) != 2 {
				t.Fatal("target outcome changed", err, target.writes, len(observations))
			}
			deploy := observations[1]
			if deploy.meta.Action != "deploy" || deploy.meta.Request != record.Intent.ID || deploy.starts != 1 || deploy.finishes != 1 || !deploy.sent {
				t.Fatal("dispatch observation lost", deploy)
			}
			if mode == "unknown" || mode == "foreign" {
				if deploy.err == nil || deploy.status != (Outcome{}) {
					t.Fatal("uncertain or foreign receipt invented", deploy)
				}
			} else {
				wantState, wantEffects := "succeeded", "complete"
				if mode == "none" {
					wantEffects = "none"
				}
				if mode == "partial" {
					wantState, wantEffects = "failed", "partial"
				}
				if deploy.err != nil || deploy.status != (Outcome{record.Intent.ID, "demo", wantState, wantEffects, "op_" + record.Intent.ID}) {
					t.Fatal("raw target tuple changed", deploy)
				}
			}
			raw, marshalErr := json.Marshal(deploy.meta)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(raw), "synthetic-static-password") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "generation") {
				t.Fatal("unsafe observer metadata")
			}
			if mode == "history-failure" {
				if len(record.Events) != 0 {
					t.Fatal("storage failure fabricated history")
				}
				return
			}
			if _, err = c.Reconcile(ctx, record, target, func(context.Context, secretbroker.Scope) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if len(observations) != 2 || target.writes != 1 {
				t.Fatal("status created another execution")
			}
		})
	}
}
func TestBrokerObservationAdmissionFailureCannotSend(t *testing.T) {
	for _, phase := range []string{"plan-factory", "plan-start", "deploy-factory", "deploy-start"} {
		t.Run(phase, func(t *testing.T) {
			c, target, i := controllerFixture(t)
			counted := &countedAdmissionTarget{controllerTarget: target}
			ctx := context.Background()
			var observation *capturedExecution
			set := func() {
				c.SetExecutionObserver(func(_ context.Context, meta ExecutionMetadata) (ExecutionObservation, error) {
					if strings.HasSuffix(phase, "factory") {
						return nil, ErrUnavailable
					}
					observation = &capturedExecution{meta: meta, start: func() error { return ErrUnavailable }}
					return observation, nil
				})
			}
			if strings.HasPrefix(phase, "plan") {
				set()
			}
			review, err := c.Prepare(ctx, i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), counted)
			if strings.HasPrefix(phase, "plan") {
				if err == nil || target.scope.Generation != "" || counted.infos != 0 || counted.requirements != 0 {
					t.Fatal("plan sent despite missing journal")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				set()
				record, err := c.Submit(ctx, review.ID, i, counted)
				rows, readErr := c.service.State.Deployments(i.Scope)
				if readErr != nil || (phase == "deploy-factory" && len(rows) != 0) || (phase == "deploy-start" && len(rows) != 1) {
					t.Fatal("incorrect durable denial ordering", readErr, len(rows))
				}
				if err == nil || target.writes != 0 {
					t.Fatal("deploy sent despite missing journal")
				}
				if phase == "deploy-start" && (len(record.Events) != 2 || record.Events[1].State != "not-admitted" || record.Events[1].Effects != "none") {
					t.Fatal("unsent intent not retained honestly", record.Events)
				}
			}
			if observation != nil && (observation.finishes != 1 || observation.sent) {
				t.Fatal("denial misreported", observation)
			}
		})
	}
}

type countedAdmissionTarget struct {
	*controllerTarget
	infos, requirements int
}

func (t *countedAdmissionTarget) Info(ctx context.Context) (TargetInfo, error) {
	t.infos++
	return t.controllerTarget.Info(ctx)
}
func (t *countedAdmissionTarget) Requirements(ctx context.Context, j, s, m, n string) (brokerwire.Requirements, error) {
	t.requirements++
	return t.controllerTarget.Requirements(ctx, j, s, m, n)
}

type panicPlanTarget struct{ *controllerTarget }

func (panicPlanTarget) Info(context.Context) (TargetInfo, error) { panic("synthetic target panic") }
func TestBrokerPlanPanicNeverReportsSuccess(t *testing.T) {
	c, target, i := controllerFixture(t)
	observation := &capturedExecution{}
	c.SetExecutionObserver(func(context.Context, ExecutionMetadata) (ExecutionObservation, error) { return observation, nil })
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected synthetic panic")
			}
		}()
		_, _ = c.Prepare(context.Background(), i, strings.Repeat("c", 32), strings.TrimPrefix(i.SourceDigest, "sha256:"), panicPlanTarget{target})
	}()
	if observation.starts != 1 || observation.finishes != 1 || !observation.sent || observation.err == nil {
		t.Fatal("panic fabricated Plan success", observation)
	}
}
