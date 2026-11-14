package api

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/target"
)

// AttendedProducer is trusted startup dependency injection, not an HTTP-facing
// source selector. Capture must return an immutable external-source capture,
// with a registered export and request-specific resolver/checkers. It is not
// implemented by the application until a separately qualified source exists.
type AttendedProducer interface {
	Capture(context.Context, string, string) (AttendedCapture, error)
}

// AttendedCapture belongs to one review. Revalidate must independently compare
// current source and target with this capture. Implementations must retain the
// original shared deployment boundary and must never copy or mutate a shared
// Runner or Coordinator. Source assertions are not proof of qualification.
type AttendedCapture interface {
	Intent() (operationhistory.SourceRef, dill.PreflightTarget)
	Revalidate(context.Context) error
	Preview(context.Context, dill.PreflightObserver) (dill.PreflightInvocation, error)
	Apply(context.Context, *dill.PreflightInvocation, dill.OperationObserver, io.Writer) (dill.OperationInvocation, error)
}

// AttendedContext is an explicitly configured, Alpha-only boundary. A nil
// producer is still configured and unavailable, never a request for legacy mode.
// It does not establish source, provider, native, or runtime qualification.
type AttendedContext struct {
	base     agent.Agent
	runner   *dill.Runner
	producer AttendedProducer
}

var errAttendedConfiguration = errors.New("invalid attended startup configuration")

// NewAttendedContext must be called before starting any background or HTTP
// work. The caller must propagate any error and abort startup. Profile must have
// been set before NewLocalAgent so AlphaReadOnly was actually installed.
func NewAttendedContext(base agent.Agent, shared *dill.Runner, producer AttendedProducer) (*AttendedContext, error) {
	if base == nil || shared == nil || base.Dill() != shared || shared.Profile != target.Alpha {
		return nil, errAttendedConfiguration
	}
	if _, ok := base.Engine().(*engine.AlphaReadOnly); !ok {
		return nil, errAttendedConfiguration
	}
	q := shared.QualifiedOperation
	if q == nil || shared.Engine != "docker" || q.Layout != "native-local-docker-v1" || !filepath.IsAbs(q.StateDir) || filepath.Clean(q.StateDir) != q.StateDir || q.DaemonID == "" || strings.TrimSpace(shared.Socket) == "" {
		return nil, errAttendedConfiguration
	}
	return &AttendedContext{base: base, runner: shared, producer: producer}, nil
}
