//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
)

// Fixture-only fake producer. No source/evaluator, Dill, engine or network
// execution occurs. All assertions are synthetic and prove no qualification.
type syntheticProducer struct{}
type syntheticCapture struct{}

func (syntheticProducer) Capture(_ context.Context, stack, action string) (api.AttendedCapture, error) {
	if stack != "observability" || action != "up" {
		return nil, errors.New("synthetic fixture supports only up")
	}
	return syntheticCapture{}, nil
}
func (syntheticCapture) Intent() (operationhistory.SourceRef, dill.PreflightTarget) {
	d := operationhistory.Digest
	return operationhistory.SourceRef{SnapshotDigest: d("fixture-source"), EvaluatorProfileDigest: d("fixture-profile"), EvaluatorRefDigest: d("fixture-interpreter")}, dill.PreflightTarget{Engine: "docker", EndpointDigest: d("fixture-endpoint"), QualifiedIdentityDigest: d("fixture-daemon")}
}
func (syntheticCapture) Revalidate(context.Context) error { return nil }
func (c syntheticCapture) Preview(_ context.Context, o dill.PreflightObserver) (dill.PreflightInvocation, error) {
	unknown := dill.PreflightInvocation{Status: "unknown"}
	if err := o.Started(); err != nil {
		return unknown, err
	}
	_, target := c.Intent()
	d := operationhistory.Digest
	input, selection := d("fixture-input"), d("fixture-selection")
	exit := 0
	result := dill.PreflightResult{Version: dill.PreflightVersion, OperationID: "op_00000000000000000000000000000001", Operation: "up", Target: &target, Stack: "observability", Outcome: "succeeded", EngineEffects: "none", CoordinationEffects: "written", InputDigest: &input, SelectionDigest: &selection, Coverage: dill.PreflightCoverage{Containers: dill.PreflightScope{Scope: "declared-stack", Complete: true}, Network: dill.PreflightScope{Scope: "stack-network", Count: 1, Complete: true}, Volumes: dill.PreflightScope{Scope: "declared-attached-named", Complete: true}}, Resources: []dill.PreflightResource{{Kind: "network", Name: "observability", Identity: "observability", IdentityKind: "planned-name", Presence: "absent", Action: "ensure", Retention: "preserve"}}, Errors: []dill.OperationDiagnostic{}}
	b, err := json.Marshal(result)
	if err != nil {
		return unknown, err
	}
	inv := dill.ObservePreflightInvocation(b, &exit)
	if inv.Result == nil {
		return unknown, errors.New("invalid synthetic review")
	}
	return inv, o.Terminal(inv)
}
func (syntheticCapture) Apply(_ context.Context, _ *dill.PreflightInvocation, o dill.OperationObserver, w io.Writer) (dill.OperationInvocation, error) {
	inv := dill.OperationInvocation{ExitCode: -1, Status: "unknown"}
	if err := o.Started(); err != nil {
		return inv, err
	}
	if err := o.Terminal(inv); err != nil {
		return inv, err
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return inv, err
	}
	_, err = fmt.Fprintln(w, dill.OperationMarker+string(b))
	return inv, err
}
func prepareSyntheticRunner(r *dill.Runner) error {
	r.Profile = "alpha"
	r.Engine = "docker"
	r.Socket = "/synthetic-fixture/no-socket"
	r.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic-fixture/no-state", Layout: "native-local-docker-v1", DaemonID: "fixture-daemon"}
	return nil
}
func mountSyntheticAttended(r chi.Router, ag agent.Agent, runner *dill.Runner, st store.StackStore, rs store.RecipeStore, gr *git.Runner, wh *web.Handler, timeouts httpx.Timeouts, data string) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(home, ".brine-attended-browser-fixture-")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (func(), error) { _ = os.RemoveAll(root); return nil, e }
	history, coord := filepath.Join(root, "history"), filepath.Join(root, "coord")
	for _, p := range []string{history, coord} {
		if err = os.Mkdir(p, 0700); err != nil {
			return fail(err)
		}
	}
	s, err := operationhistory.Open(history, data, coord)
	if err != nil {
		return fail(err)
	}
	boundary, err := api.NewAttendedContext(ag, runner, syntheticProducer{})
	if err != nil {
		_ = s.Close()
		return fail(err)
	}
	if err = api.MountAttended(r, ag, st, rs, gr, "docker", timeouts, 30*time.Second, boundary, s); err != nil {
		_ = s.Close()
		return fail(err)
	}
	if err = web.MountAttended(r, wh, timeouts, boundary); err != nil {
		_ = s.Close()
		return fail(err)
	}
	return func() { _ = s.Close(); _ = os.RemoveAll(root) }, nil
}
