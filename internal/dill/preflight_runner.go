package dill

import (
	"context"
	"io"
	"time"
)

// PreflightObserver is per-request durable history, not a shared Runner field.
// Started must commit intent before capability discovery; Terminal persists the
// independent process/result axes. It does not inspect or acknowledge a journal.
type PreflightObserver interface {
	Started() error
	Terminal(PreflightInvocation) error
}

// PreflightHistoryError retains the independently observed axes when history
// could not be durably persisted. It never authorizes confirmation or retry.
type PreflightHistoryError struct{ Invocation PreflightInvocation }

func (e *PreflightHistoryError) Error() string {
	return "Preflight history persistence failed; inspect request status before a new attempt"
}

// Presence is a fail-closed configuration prerequisite, not source proof.
func (r *Runner) checkPreflightGuards() error {
	if r.CheckSource == nil || r.BeforeLegacyAction == nil {
		return ErrPreflightContract
	}
	return nil
}

func (r *Runner) checkPreflightCapabilities(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, exit, err := r.operationCapture(ctx, 8192, "preflight-capabilities", "--format", "json")
	if err != nil || exit == nil || *exit != 0 {
		return ErrPreflightContract
	}
	caps, err := DecodePreflightCapabilities(b)
	if err != nil || !caps.Supported {
		return ErrPreflightContract
	}
	return nil
}

// PreviewPreflight is dormant until a controller supplies durable history and
// an independently trusted binding. It does not establish source provenance,
// confirmation authority, native placement or actual provider qualification.
func (r *Runner) PreviewPreflight(ctx context.Context, path, op string, forceRecreate bool, binding PreflightBinding, observer PreflightObserver) (v PreflightInvocation, err error) {
	v.Status = "unknown"
	if observer == nil {
		return v, ErrPreflightContract
	}
	startedFailed := false
	defer func() {
		if observer.Terminal(v) != nil || startedFailed {
			err = &PreflightHistoryError{Invocation: v}
		}
	}()
	if observer.Started() != nil {
		startedFailed = true
		return v, ErrPreflightContract
	}
	if r.checkPreflightGuards() != nil {
		return v, ErrPreflightContract
	}
	if !preflightAction(op) || ValidateQualifiedOperationConfig(r.Engine, r.Socket, r.QualifiedOperation) != nil || r.ResolveStack == nil {
		return v, ErrPreflightContract
	}
	q := r.QualifiedOperation
	endpoint, e := normalizeOperationSocket(r.Socket)
	if e != nil {
		return v, ErrPreflightContract
	}
	stack, e := r.ResolveStack(path)
	if e != nil || binding.Stack != stack || binding.Operation != op || binding.Engine != r.Engine || binding.EndpointDigest != operationDigest(endpoint) || binding.QualifiedIdentityDigest != operationDigest("dill-local-docker-v1\x00"+q.DaemonID) {
		return v, ErrPreflightBinding
	}
	if r.checkSource(path) != nil || (r.StrictActions != nil && r.BeforeLegacyAction == nil) {
		return v, ErrPreflightContract
	}
	if r.BeforeLegacyAction != nil && r.BeforeLegacyAction(ctx, path) != nil {
		return v, ErrPreflightContract
	}
	args, e := BuildPreflightArguments(PreflightArguments{Operation: op, ConfigFile: path, Socket: endpoint, Stack: stack, Coordination: *q, ForceRecreate: forceRecreate})
	if e != nil {
		return v, e
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if r.checkOperationCapabilities(ctx, true) != nil || r.checkPreflightCapabilities(ctx) != nil {
		return v, ErrPreflightContract
	}
	b, exit, e := r.operationCapture(ctx, PreflightMaxOutputBytes, args...)
	if e != nil {
		if exit != nil {
			v.ProcessExit = exit
		}
		return v, ErrPreflightContract
	}
	v = ObservePreflightInvocation(b, exit)
	if v.Result == nil {
		return v, ErrPreflightContract
	}
	result := v.Result
	if (result.Operation != "" && result.Operation != op) || (result.Stack != "" && result.Stack != stack) || (result.Target != nil && (result.Stack != stack || result.Target.Engine != binding.Engine || result.Target.EndpointDigest != binding.EndpointDigest || result.Target.QualifiedIdentityDigest != binding.QualifiedIdentityDigest)) {
		v.Result = nil
		v.Status = "unknown"
		return v, ErrPreflightBinding
	}
	if v.Status != "succeeded" {
		return v, ErrPreflightContract
	}
	if CheckPreflightBinding(&v, binding) != nil {
		v.Result = nil
		v.Status = "unknown"
		return v, ErrPreflightBinding
	}
	return v, nil
}

// ApplyBoundPreflight performs one fresh provider comparison and one operation;
// it never falls back, retries, replays, or selects resources locally. The caller
// must separately enforce immutable captured-source and confirmation authority.
// WithOperationObserver is mandatory even when legacy observation is optional.
func (r *Runner) ApplyBoundPreflight(ctx context.Context, path string, review *PreflightInvocation, binding PreflightBinding, forceRecreate bool, w io.Writer) (OperationInvocation, error) {
	if !preflightAction(binding.Operation) {
		return OperationInvocation{Status: "unknown", ExitCode: -1}, ErrPreflightBinding
	}
	defer r.lockPath(path)()
	return r.operationMode(ctx, path, binding.Operation, w, &boundOperation{review: review, binding: binding, forceRecreate: forceRecreate})
}
