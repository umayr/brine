package remoteui

import (
	"context"
	"errors"
	"sync"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

type ownedReadSession interface {
	ReadSession
	Close() error
}
type pinnedOpener func(context.Context, targetregistry.Record, remoteagent.Config) (ownedReadSession, error)

// RegistryLeaseProvider holds the registry shared flock for the entire lifetime
// of the verified session, blocking all cooperating registry writers. Registry
// ancestors, filesystem semantics and same-user writers remain trusted as
// described by targetregistry and agenttransport.
type RegistryLeaseProvider struct {
	registry *targetregistry.Registry
	config   remoteagent.Config
	open     pinnedOpener
}

func NewRegistryLeaseProvider(r *targetregistry.Registry, c remoteagent.Config) (*RegistryLeaseProvider, error) {
	if r == nil || c.Timeout <= 0 {
		return nil, errors.New("registry and positive timeout required")
	}
	return &RegistryLeaseProvider{r, c, func(ctx context.Context, record targetregistry.Record, cfg remoteagent.Config) (ownedReadSession, error) {
		return remoteagent.Open(ctx, record, cfg)
	}}, nil
}
func (p *RegistryLeaseProvider) Acquire(ctx context.Context, id string) (context.Context, ReadSession, func() error, error) {
	if ctx == nil || p == nil || p.registry == nil || p.open == nil {
		return nil, nil, nil, errors.New("lease unavailable")
	}
	record, lease, err := p.registry.AcquireRead(ctx, id)
	if err != nil {
		if errors.Is(err, targetregistry.ErrCleanup) {
			err = errors.Join(ErrReadCleanup, err)
		}
		return nil, nil, nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	session, err := p.open(lifetime, record, p.config)
	if err != nil {
		cancel()
		if errors.Is(err, remoteagent.ErrCleanup) {
			err = errors.Join(ErrReadCleanup, err)
		}
		return nil, nil, nil, errors.Join(err, cleanupFailure(lease.Close()))
	}
	if lifetime.Err() != nil {
		cancel()
		return nil, nil, nil, errors.Join(lifetime.Err(), cleanupFailure(errors.Join(session.Close(), lease.Close())))
	}
	var once sync.Once
	var closeErr error
	release := func() error {
		once.Do(func() { cancel(); closeErr = cleanupFailure(errors.Join(session.Close(), lease.Close())) })
		return closeErr
	}
	return lifetime, session, release, nil
}
