// Package remoteagent composes a pinned transport and a verified agent session.
// It does not enforce registry freshness or authorize interactive actions.
package remoteagent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/targetregistry"
)

var ErrInventoryLimit = errors.New("remoteagent: inventory pagination limit exceeded or cursor repeated")

// ErrCleanup marks failed cleanup during session setup.
var ErrCleanup = errors.New("remoteagent: cleanup failed")

func cleanupError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrCleanup, err)
}

// Config supplies local transport settings; all endpoint details come from Record.
type Config struct {
	LocalDir string
	Timeout  time.Duration
}

type forward interface {
	Context() context.Context
	Socket() string
	Close() error
}
type client interface {
	Session(context.Context) (string, error)
	Inventory(context.Context, string, *string, int) (agentclient.InventoryPage, error)
	Inspect(context.Context, string, string) (agentclient.InspectResult, error)
	Logs(context.Context, string, string, agentclient.LogOptions, func(context.Context, agentclient.LogEvent) error) error
	Close() error
}
type dependencies struct {
	open      func(context.Context, agenttransport.Config) (forward, error)
	newClient func(context.Context, string, agentclient.Binding) (client, error)
}

// Open verifies a fresh challenge on the exact forward. ctx owns the forward's
// lifetime and must be cancelled on registry updates. The record must be
// rechecked by the caller before use and before delivering results.
func Open(ctx context.Context, r targetregistry.Record, cfg Config) (*Session, error) {
	return open(ctx, r, cfg, dependencies{
		open: func(ctx context.Context, c agenttransport.Config) (forward, error) {
			return agenttransport.Open(ctx, c)
		},
		newClient: func(ctx context.Context, s string, b agentclient.Binding) (client, error) {
			return agentclient.New(ctx, s, b)
		},
	})
}

type httpsPinMismatchError struct{}

func (httpsPinMismatchError) Error() string { return "remoteagent: target unavailable" }
func (httpsPinMismatchError) Unwrap() error { return agentclient.ErrTLSPinMismatch }

func open(ctx context.Context, r targetregistry.Record, cfg Config, d dependencies) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("remoteagent: nil context")
	}
	if r.HTTPSAddress != "" {
		if r.Validate() != nil {
			return nil, errors.New("remoteagent: invalid target")
		}
		p, err := agentclient.ParsePairing([]byte(r.HTTPSPairing))
		if err != nil {
			return nil, errors.New("remoteagent: target unavailable")
		}
		request := ctx
		if cfg.Timeout > 0 {
			var cancel context.CancelFunc
			request, cancel = context.WithTimeout(ctx, cfg.Timeout)
			defer cancel()
		}
		c, epoch, err := p.Open(ctx, request, r.HTTPSAddress)
		if err != nil || epoch == "" {
			if c != nil {
				_ = c.Close()
			}
			if errors.Is(err, agentclient.ErrTLSPinMismatch) {
				return nil, httpsPinMismatchError{}
			}
			return nil, errors.New("remoteagent: target unavailable")
		}
		return &Session{client: c, epoch: epoch}, nil
	}
	b, err := r.Binding()
	if err != nil {
		return nil, err
	}
	f, err := d.open(ctx, agenttransport.Config{Address: net.JoinHostPort(r.Host, strconv.Itoa(int(r.Port))), User: r.User, KeyPath: r.KeyPath, HostPublicKey: r.HostPublicKey, RemoteSocket: r.RemoteSocket, LocalDir: cfg.LocalDir, Timeout: cfg.Timeout})
	if err != nil {
		if errors.Is(err, agenttransport.ErrCleanup) {
			err = errors.Join(ErrCleanup, err)
		}
		return nil, err
	}
	c, err := d.newClient(f.Context(), f.Socket(), b)
	if err != nil {
		return nil, errors.Join(err, cleanupError(f.Close()))
	}
	epoch, err := c.Session(ctx)
	if err != nil || epoch == "" {
		if err == nil {
			err = errors.New("remoteagent: empty epoch")
		}
		return nil, errors.Join(err, cleanupError(errors.Join(c.Close(), f.Close())))
	}
	if ctx.Err() != nil || f.Context().Err() != nil {
		return nil, errors.Join(errors.New("remoteagent: forward closed"), cleanupError(errors.Join(c.Close(), f.Close())))
	}
	return &Session{forward: f, client: c, epoch: epoch}, nil
}

// Observe verifies the SSH host key and signed agent/engine binding on a fresh
// pinned connection. It does not verify the reveal-verifier fingerprint, local
// key path identity, or other independently reviewed fields. It returns the
// supplied record unchanged, not discovered fields, and closes the connection.
// It does not approve enrollment or establish registry freshness.
func Observe(ctx context.Context, r targetregistry.Record, cfg Config) (targetregistry.Record, error) {
	return observe(ctx, r, cfg, Open)
}

func observe(ctx context.Context, r targetregistry.Record, cfg Config, opener func(context.Context, targetregistry.Record, Config) (*Session, error)) (targetregistry.Record, error) {
	s, err := opener(ctx, r, cfg)
	if err != nil {
		return targetregistry.Record{}, err
	}
	if err := s.Close(); err != nil {
		return targetregistry.Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return targetregistry.Record{}, err
	}
	return r, nil
}

// Session owns the verified connection. Successful Open verifies the pinned SSH
// host key and signed agent/engine binding, not enrollment or user consent.
type Session struct {
	forward  forward
	client   client
	epoch    string
	once     sync.Once
	closeErr error
}

func (s *Session) Close() error {
	s.once.Do(func() {
		s.closeErr = s.client.Close()
		if s.forward != nil {
			s.closeErr = errors.Join(s.closeErr, s.forward.Close())
		}
	})
	return s.closeErr
}
func (s *Session) fail(err error) error {
	if err != nil {
		return errors.Join(err, s.Close())
	}
	return nil
}

// Inventory returns all pages or an error, never a partial result. maxPages is
// an explicit resource bound; a further cursor or repeated cursor fails closed.
func (s *Session) Inventory(ctx context.Context, maxPages int) ([]agentclient.InventoryItem, error) {
	if maxPages < 1 || maxPages > 1000 {
		return nil, s.fail(errors.New("remoteagent: invalid page bound"))
	}
	var out []agentclient.InventoryItem
	var cursor *string
	seen := map[string]bool{}
	lastID := ""
	for i := 0; i < maxPages; i++ {
		p, err := s.client.Inventory(ctx, s.epoch, cursor, 100)
		if err != nil {
			return nil, s.fail(err)
		}
		for _, item := range p.Items {
			if item.ID == "" || (lastID != "" && item.ID <= lastID) {
				return nil, s.fail(errors.New("remoteagent: inventory IDs not strictly increasing"))
			}
			lastID = item.ID
		}
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out, nil
		}
		next := *p.NextCursor
		if next == "" || seen[next] {
			return nil, s.fail(ErrInventoryLimit)
		}
		seen[next] = true
		cursor = &next
	}
	return nil, s.fail(ErrInventoryLimit)
}
func (s *Session) Inspect(ctx context.Context, id string) (agentclient.InspectResult, error) {
	v, err := s.client.Inspect(ctx, s.epoch, id)
	if err != nil {
		return agentclient.InspectResult{}, s.fail(err)
	}
	return v, nil
}

// Logs is synchronous; emit must return before Logs returns. Cancellation and
// callback errors terminate the session rather than redialing.
func (s *Session) Logs(ctx context.Context, id string, opts agentclient.LogOptions, emit func(context.Context, agentclient.LogEvent) error) error {
	return s.fail(s.client.Logs(ctx, s.epoch, id, opts, emit))
}
