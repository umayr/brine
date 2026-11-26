package remoteui

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

// ObservationSession exposes only the exact leased owner digest, never pins
// or pairing credentials. Its read methods remain on the same verified session.
type ObservationSession interface {
	ReadSession
	ObservationBinding() string
	Capabilities(context.Context) ([]string, error)
}
type observationReadSession struct {
	ReadSession
	binding string
}

func (s observationReadSession) ObservationBinding() string { return s.binding }
func (s observationReadSession) Capabilities(ctx context.Context) ([]string, error) {
	c, ok := s.ReadSession.(interface {
		Capabilities(context.Context) ([]string, error)
	})
	if !ok {
		return nil, errors.New("capabilities unavailable")
	}
	return c.Capabilities(ctx)
}

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
	binding := ""
	if record.HTTPSAddress != "" {
		binding = fmt.Sprintf("%x", sha256.Sum256([]byte(record.HTTPSAddress+"\x00"+string(record.HTTPSPairing))))
	}
	return lifetime, observationReadSession{ReadSession: session, binding: binding}, release, nil
}
