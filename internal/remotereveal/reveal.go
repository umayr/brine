// Package remotereveal implements an unmounted, synchronous reveal executor.
// It does not authenticate requests or provide an HTTP handler.
package remotereveal

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/agenttransport"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

var ErrDenied = errors.New("remote reveal denied")

// ErrCleanup indicates resources may not have been fully released. It never
// includes underlying transport, filesystem, signer, or agent error details.
var ErrCleanup = errors.New("remote reveal cleanup failed")

func cleanupFailure(err error) error {
	if err != nil {
		return errors.Join(ErrDenied, ErrCleanup)
	}
	return ErrDenied
}

// Signer must protect its private key, honor ctx synchronously, and sign the
// supplied message with Ed25519. The executor verifies the returned signature.
type Signer interface {
	PublicKey() ed25519.PublicKey
	Sign(context.Context, []byte) ([]byte, error)
}
type Audit interface {
	BeginRevealAudit(context.Context, string, string, string) (revealaudit.Intent, error)
	Finish(context.Context, string, revealaudit.Outcome) error
}
type Session interface {
	Epoch() string
	Reveal(context.Context, string, string, agentclient.Grant) (agentclient.RevealResult, error)
	Close() error
}

// OpenSession must create a new pinned, challenge-verified session each call.
// Replacing it is a trusted test/integration seam, not an authentication API.
type OpenSession func(context.Context, targetregistry.Record, ed25519.PublicKey) (Session, error)

type Executor struct {
	Registry *targetregistry.Registry
	Audit    Audit
	Signer   Signer
	Open     OpenSession
}

// Submission is trusted integration input, not proof of authentication. The
// caller MUST first use auth middleware on a state-changing CSRF-protected
// confirmation request, require CookieAuthenticated and acquire SubmissionLease.
// Context must be that lease's context; Release must be its release function.
// Execute owns and releases the lease, including on failure. Logout waits while
// this submission is held, per auth.SubmissionLease; cancellation/expiry deny.
// AdminID is the trusted auth.Identity, never a request parameter.
type Submission struct {
	Context context.Context
	Release func()
	AdminID string
}

// Execute requires the exact record shown at confirmation. It holds the registry
// read lease and submission lease through synchronous delivery. deliver must not
// retain values, spawn work, log them, or write after ctx cancellation. As with
// any stream, bytes already delivered cannot be recalled. Caller must prevent
// multiple invocations of a Submission and must not release it independently.
func (e *Executor) Execute(s Submission, reviewed targetregistry.Record, containerID string, deliver func(context.Context, agentclient.RevealResult) error) (ret error) {
	if s.Release == nil {
		return ErrDenied
	}
	defer s.Release()
	ctx := s.Context
	if ctx == nil || ctx.Err() != nil || e == nil || e.Registry == nil || e.Audit == nil || e.Signer == nil || e.Open == nil || deliver == nil || s.AdminID == "" {
		return ErrDenied
	}
	r, lease, err := e.Registry.AcquireRead(ctx, reviewed.ID)
	if err != nil {
		if errors.Is(err, targetregistry.ErrCleanup) {
			return cleanupFailure(ErrCleanup)
		}
		return ErrDenied
	}
	defer func() {
		if lease.Close() != nil {
			ret = cleanupFailure(ErrCleanup)
		}
	}()
	if r.Verify(reviewed) != nil || ctx.Err() != nil {
		return ErrDenied
	}
	key, err := r.VerifiedRevealKey(e.Signer.PublicKey())
	if err != nil || ctx.Err() != nil {
		return ErrDenied
	}
	session, err := e.Open(ctx, r, key)
	if session != nil {
		defer func() {
			if session != nil && session.Close() != nil {
				ret = cleanupFailure(ErrCleanup)
			}
		}()
	}
	if err != nil || session == nil {
		if errors.Is(err, ErrCleanup) {
			return cleanupFailure(ErrCleanup)
		}
		return ErrDenied
	}
	epoch := session.Epoch()
	intent, err := e.Audit.BeginRevealAudit(ctx, s.AdminID, r.ID, containerID)
	if err != nil {
		return ErrDenied
	}
	outcome := revealaudit.Failed
	// A canceled context intentionally leaves an auditable pending intent.
	finish := func() bool { return e.Audit.Finish(ctx, intent.Correlation, outcome) == nil && ctx.Err() == nil }
	fail := func() error { _ = finish(); return ErrDenied }
	request, err := agentclient.NewRequestID()
	if err != nil {
		return fail()
	}
	random := func(n int) (string, error) {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	nonce, err := random(32)
	if err != nil {
		return fail()
	}
	// This random grant identifier is not an authenticated user-session identity;
	// user attribution is supplied by the trusted submission and audit record.
	sid, err := random(16)
	if err != nil {
		return fail()
	}
	now := time.Now().Unix()
	if now < 0 || ctx.Err() != nil {
		return fail()
	}
	g := agentclient.Grant{AgentID: r.AgentInstallationID, EngineID: r.EngineID, Generation: r.Generation, Epoch: epoch, ContainerID: containerID, SessionID: sid, Nonce: nonce, RequestID: request, IssuedAt: uint64(now), ExpiresAt: uint64(now) + 30, Scope: "env.values", Interactive: true}
	msg, err := agentclient.GrantBytes(g)
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	sig, err := e.Signer.Sign(ctx, msg)
	if err != nil || ctx.Err() != nil || !ed25519.Verify(key, msg, sig) {
		return fail()
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(sig)
	if ctx.Err() != nil {
		return fail()
	}
	result, err := session.Reveal(ctx, epoch, containerID, g)
	if err != nil || ctx.Err() != nil {
		return fail()
	}
	// The concrete agentclient performs complete response validation; test openers
	// are trusted to enforce the same contract.
	if result.ContainerID != containerID || result.Env == nil {
		return fail()
	}
	err = session.Close()
	session = nil
	if err != nil {
		_ = fail()
		return cleanupFailure(err)
	}
	outcome = revealaudit.Succeeded
	if !finish() || ctx.Err() != nil {
		return ErrDenied
	}
	if deliver(ctx, result) != nil {
		return ErrDenied
	}
	if ctx.Err() != nil {
		return ErrDenied
	}
	return nil
}

// PinnedOpener constructs a fresh single-use SSH forward and v2 reveal client.
type forwarding interface {
	Context() context.Context
	Socket() string
	Close() error
}
type revealClient interface {
	Session(context.Context) (string, error)
	Reveal(context.Context, string, string, agentclient.Grant) (agentclient.RevealResult, error)
	Close() error
}
type openDeps struct {
	forward func(context.Context, agenttransport.Config) (forwarding, error)
	client  func(context.Context, string, agentclient.Binding, ed25519.PublicKey) (revealClient, error)
}

func PinnedOpener(cfg remoteagent.Config) OpenSession {
	return pinnedOpener(cfg, openDeps{
		forward: func(ctx context.Context, cfg agenttransport.Config) (forwarding, error) {
			return agenttransport.Open(ctx, cfg)
		},
		client: func(ctx context.Context, socket string, b agentclient.Binding, key ed25519.PublicKey) (revealClient, error) {
			return agentclient.NewWithRevealKey(ctx, socket, b, key)
		},
	})
}
func pinnedOpener(cfg remoteagent.Config, d openDeps) OpenSession {
	return func(ctx context.Context, r targetregistry.Record, key ed25519.PublicKey) (Session, error) {
		binding, err := r.Binding()
		if err != nil {
			return nil, ErrDenied
		}
		f, err := d.forward(ctx, agenttransport.Config{Address: net.JoinHostPort(r.Host, strconv.Itoa(int(r.Port))), User: r.User, KeyPath: r.KeyPath, HostPublicKey: r.HostPublicKey, RemoteSocket: r.RemoteSocket, LocalDir: cfg.LocalDir, Timeout: cfg.Timeout})
		if err != nil || f == nil {
			var closeErr error
			if f != nil {
				closeErr = f.Close()
			}
			if errors.Is(err, agenttransport.ErrCleanup) || closeErr != nil {
				return nil, cleanupFailure(ErrCleanup)
			}
			return nil, ErrDenied
		}
		c, err := d.client(f.Context(), f.Socket(), binding, key)
		if err != nil || c == nil {
			var closeErr error
			if c != nil {
				closeErr = c.Close()
			}
			return nil, cleanupFailure(errors.Join(closeErr, f.Close()))
		}
		epoch, err := c.Session(ctx)
		if err != nil || epoch == "" || ctx.Err() != nil || f.Context().Err() != nil {
			return nil, cleanupFailure(errors.Join(c.Close(), f.Close()))
		}
		return &pinned{client: c, forward: f, epoch: epoch}, nil
	}
}

type pinned struct {
	client  revealClient
	forward forwarding
	epoch   string
}

func (p *pinned) Epoch() string { return p.epoch }
func (p *pinned) Reveal(ctx context.Context, epoch, id string, g agentclient.Grant) (agentclient.RevealResult, error) {
	return p.client.Reveal(ctx, epoch, id, g)
}
func (p *pinned) Close() error { return errors.Join(p.client.Close(), p.forward.Close()) }
