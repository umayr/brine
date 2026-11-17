package remoteui

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

// ReadSession is one verified agent session on its exact owned forward.
type ReadSession interface {
	Inventory(context.Context, int) ([]agentclient.InventoryItem, error)
	Inspect(context.Context, string) (agentclient.InspectResult, error)
	Logs(context.Context, string, agentclient.LogOptions, func(context.Context, agentclient.LogEvent) error) error
}

// ReadLeaseProvider must hold a process-shared registry read lease excluding all
// writers from before record selection/open through release. The returned
// session must be freshly verified against the exact leased record. Release
// cancels and closes the session before releasing the registry lease.
type ReadLeaseProvider interface {
	Acquire(context.Context, string) (context.Context, ReadSession, func() error, error)
}

type ReadBackend struct {
	registry *targetregistry.Registry
	leases   ReadLeaseProvider
}

// ErrReadCleanup marks teardown failures without exposing their details to logs.
var ErrReadCleanup = errors.New("remote read cleanup failed")

func cleanupFailure(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrReadCleanup, err)
}

type readSessionKey struct{}
type boundRead struct {
	host    string
	session ReadSession
}

// NewReadBackend constructs an unmounted adapter. A provider implementing the
// complete cross-process lease contract is mandatory; no fallback exists.
func NewReadBackend(r *targetregistry.Registry, p ReadLeaseProvider) (*ReadBackend, error) {
	if r == nil || p == nil {
		return nil, errors.New("registry and read lease provider required")
	}
	return &ReadBackend{r, p}, nil
}
func (b *ReadBackend) AcquireRead(ctx context.Context, id string) (context.Context, func() error, error) {
	if id == "" {
		return nil, nil, errors.New("missing target")
	}
	lease, session, release, err := b.leases.Acquire(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if lease == nil || session == nil || release == nil {
		if release != nil {
			return nil, nil, errors.Join(errors.New("invalid read lease"), cleanupFailure(release()))
		}
		return nil, nil, errors.New("invalid read lease")
	}
	if lease.Err() != nil {
		return nil, nil, errors.Join(errors.New("expired read lease"), cleanupFailure(release()))
	}
	return context.WithValue(lease, readSessionKey{}, boundRead{id, session}), release, nil
}
func sessionFor(ctx context.Context, id string) (ReadSession, error) {
	v, ok := ctx.Value(readSessionKey{}).(boundRead)
	if !ok || ctx.Err() != nil || id == "" || v.host != id || v.session == nil {
		return nil, errors.New("verified lease required")
	}
	return v.session, nil
}
func (b *ReadBackend) Hosts(ctx context.Context) ([]Host, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	records, err := b.registry.ListContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(records))
	for _, r := range records {
		out = append(out, Host{ID: r.ID, Name: r.ID, State: "enrolled"})
	}
	return out, ctx.Err()
}
func (*ReadBackend) Inventory(ctx context.Context, id string) ([]Container, error) {
	s, err := sessionFor(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := s.Inventory(ctx, 100)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return mapInventory(v), nil
}
func (*ReadBackend) Inspect(ctx context.Context, id, container string) (Inspection, error) {
	s, err := sessionFor(ctx, id)
	if err != nil {
		return Inspection{}, err
	}
	v, err := s.Inspect(ctx, container)
	if err != nil {
		return Inspection{}, err
	}
	if ctx.Err() != nil {
		return Inspection{}, ctx.Err()
	}
	return mapInspection(v, container)
}
func (*ReadBackend) Logs(ctx context.Context, id, container string, tail int, follow bool) (LogStream, error) {
	s, err := sessionFor(ctx, id)
	if err != nil {
		return nil, err
	}
	if !fullID(container) || tail < 1 || tail > 1000 {
		return nil, errors.New("invalid logs request")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	st := &readLogStream{cancel: cancel, events: make(chan LogEvent), done: make(chan struct{})}
	go func() {
		defer close(st.done)
		defer close(st.events)
		st.err = s.Logs(streamCtx, container, agentclient.LogOptions{Tail: tail, Follow: follow}, func(cb context.Context, e agentclient.LogEvent) error {
			event := LogEvent{Type: e.Type, Stream: e.Stream, Bytes: e.Bytes, Reason: e.Reason}
			select {
			case st.events <- event:
				return nil
			case <-cb.Done():
				return cb.Err()
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
	}()
	return st, nil
}

type readLogStream struct {
	cancel context.CancelFunc
	events chan LogEvent
	done   chan struct{}
	err    error
	once   sync.Once
}

func (s *readLogStream) Next(ctx context.Context) (LogEvent, error) {
	select {
	case <-ctx.Done():
		return LogEvent{}, ctx.Err()
	case event, ok := <-s.events:
		if ok {
			return event, nil
		}
		<-s.done
		if s.err != nil {
			return LogEvent{}, s.err
		}
		return LogEvent{}, io.EOF
	}
}
func (s *readLogStream) Close() error { s.once.Do(s.cancel); <-s.done; return nil }
func (*ReadBackend) Reveal(context.Context, string, string, Confirmation) ([]Env, error) {
	return nil, errors.New("reveal disabled")
}
func (*ReadBackend) Audit(context.Context, string, bool) {}
func (*ReadBackend) BeginRevealAudit(context.Context, RevealIntent) (string, error) {
	return "", errors.New("reveal disabled")
}
func (*ReadBackend) FinishRevealAudit(context.Context, string, bool) error {
	return errors.New("reveal disabled")
}
