// Package projectstaging provides dormant, explicitly confirmed snapshot staging.
// Callers must authorize access, explicitly warn that snapshots may contain
// secrets, protect review metadata, and provide freshly resolved authoritative
// configuration on every operation. The sender neither scans nor authorizes.
// Callers must hold a shared configuration lock or lease, honored by all writers,
// across each entire Create, Confirm, and Stage call. In particular the Stage
// lease must remain held through submission and return; without it callers must
// not invoke Stage. This trusted-caller obligation cannot be enforced here.
// SSH authenticates the host, not the remote service or engine; local socket
// access is not target attestation. Operators must trust the target. Staging
// transfers captured bytes only; it never authorizes execution or deployment.
// Existing secret and downgrade guards remain mandatory for future operations.
package projectstaging

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
)

var ErrInvalid = errors.New("snapshot review or target unavailable")
var ErrUnsupported = errors.New("remote snapshot staging unsupported")

// Resolver must freshly resolve authoritative, non-secret configuration each time.
// Options["engine"] must contain the fixed service engine and is review-bound.
type Resolver func(context.Context) (projectreview.Current, error)

type Sender struct {
	store    *projectreview.Store
	target   dillserve.Target
	identity string
	resolve  Resolver
	ssh      *dillserve.PinnedSSHOptions
	openSSH  func(context.Context, dillserve.PinnedSSHOptions) (stagingClient, error)
}

type stagingClient interface {
	Status(context.Context) (dillserve.Status, error)
	Health(context.Context) (dillserve.Health, error)
	Stage(context.Context, []byte) (string, error)
	Close()
}

func New(store *projectreview.Store, target dillserve.Target, identity string, resolve Resolver) (*Sender, error) {
	if store == nil || resolve == nil || identity == "" || target.Stack == "" || target.Engine == "" || !filepath.IsAbs(target.Socket) || filepath.Clean(target.Socket) != target.Socket || strings.IndexByte(target.Socket, 0) >= 0 {
		return nil, ErrInvalid
	}
	return &Sender{store: store, target: target, identity: identity, resolve: resolve}, nil
}

// NewPinnedSSH binds trusted operator-selected transport settings to review.
// The identity file and parent directories must remain trusted throughout use.
func NewPinnedSSH(store *projectreview.Store, identity string, o dillserve.PinnedSSHOptions, resolve Resolver) (*Sender, error) {
	if store == nil || resolve == nil || identity == "" || o.Stack == "" || o.Engine == "" || !filepath.IsAbs(o.RemoteSocket) || filepath.Clean(o.RemoteSocket) != o.RemoteSocket || dillserve.ValidatePinnedSSH(o) != nil {
		return nil, ErrInvalid
	}
	s := &Sender{store: store, identity: identity, resolve: resolve, ssh: &o}
	s.openSSH = func(ctx context.Context, opts dillserve.PinnedSSHOptions) (stagingClient, error) {
		return dillserve.NewPinnedSSH(ctx, opts)
	}
	return s, nil
}
func failure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrInvalid
}

func (s *Sender) current(ctx context.Context) (projectreview.Current, error) {
	c, err := s.resolve(ctx)
	if err != nil {
		return c, failure(ctx)
	}
	return s.check(c)
}

// Create validates the preloaded snapshot before exposing a review. It does not read files.
func (s *Sender) Create(ctx context.Context, session string, snapshot projectsnapshot.Snapshot) (projectreview.Preview, error) {
	c, err := s.current(ctx)
	if err != nil {
		return projectreview.Preview{}, err
	}
	if projectsnapshot.ValidateProvisionalReceiver(ctx, snapshot) != nil {
		return projectreview.Preview{}, failure(ctx)
	}
	p, err := s.store.Create(ctx, session, projectreview.Intent{Snapshot: snapshot, Current: c})
	if err != nil {
		return projectreview.Preview{}, failure(ctx)
	}
	return p, nil
}

// Confirm is a separate explicit consent action; callers must present the full preview first.
func (s *Sender) Confirm(ctx context.Context, session, id string) (string, error) {
	c, err := s.resolve(ctx)
	if err != nil {
		return "", failure(ctx)
	}
	// Confirm even on a changed target so the store revokes mismatched previews.
	token, err := s.store.Confirm(ctx, session, id, c)
	if s.ssh == nil && c.Target.Kind == projectreview.SSH {
		return "", ErrUnsupported
	}
	if err != nil {
		return "", failure(ctx)
	}
	if _, err = s.check(c); err != nil {
		return "", err
	}
	return token, nil
}
func (s *Sender) check(c projectreview.Current) (projectreview.Current, error) {
	t := c.Target
	if s.ssh != nil {
		o := *s.ssh
		if dillserve.ValidatePinnedSSH(o) != nil || t.Kind != projectreview.SSH || t.Identity != s.identity || t.Socket != o.RemoteSocket || t.Endpoint != o.Host || t.SSHHost != o.Host || t.SSHUser != o.User || t.SSHPort != o.Port || t.SSHIdentityFile != o.IdentityFile || t.SSHPublicKey != o.PublicKey || t.SSHKnownHost != o.Fingerprint || c.Stack != o.Stack || c.Options["engine"] != o.Engine {
			return c, ErrInvalid
		}
		return c, nil
	}
	if t.Kind == projectreview.SSH {
		return c, ErrUnsupported
	}
	if t.Kind != projectreview.Local || t.Identity != s.identity || t.Socket != s.target.Socket || t.Endpoint != "" || t.SSHKnownHost != "" || t.SSHHost != "" || t.SSHUser != "" || t.SSHPort != 0 || t.SSHIdentityFile != "" || t.SSHPublicKey != "" || c.Stack != s.target.Stack || c.Options["engine"] != s.target.Engine {
		return c, ErrInvalid
	}
	return c, nil
}

type wireFile struct {
	Path   string `json:"path"`
	Bytes  []byte `json:"bytes"`
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}
type wire struct {
	Schema     string     `json:"schema"`
	Entrypoint string     `json:"entrypoint"`
	Files      []wireFile `json:"files"`
}

// Stage atomically consumes one-shot consent at Store.Consume after resolving
// configuration, before opening a tunnel or uploading. That is the consent
// linearization point, not a lock on configuration: the caller's mandatory
// writer-excluding lease must cover the entire call, including submission.
// A fresh lookup alone cannot prevent in-flight or unobserved ABA drift. No retry.
func (s *Sender) Stage(ctx context.Context, session, id, token string) (string, error) {
	return s.StageWithSubmissionLease(ctx, session, id, token, nil)
}

// StageWithSubmissionLease acquires an optional authorization lease after all
// receiver negotiation and immediately before submission, holding it until the
// request finishes. Failure consumes the review without sending snapshot bytes.
// This is separate from, and does not replace, the mandatory caller-held
// writer-excluding configuration lease covering the entire operation.
func (s *Sender) StageWithSubmissionLease(ctx context.Context, session, id, token string, acquire func(context.Context) (context.Context, func(), error)) (string, error) {
	c, err := s.resolve(ctx)
	if err != nil {
		return "", failure(ctx)
	}
	// Consume first, even for SSH drift: a valid token must revoke the
	// reviewed local intent when authoritative current configuration changes.
	intent, err := s.store.Consume(ctx, session, id, token, c)
	if s.ssh == nil && c.Target.Kind == projectreview.SSH {
		return "", ErrUnsupported
	}
	if err != nil {
		return "", failure(ctx)
	}
	if _, err = s.check(c); err != nil {
		return "", err
	}
	if projectsnapshot.ValidateProvisionalReceiver(ctx, intent.Snapshot) != nil {
		return "", failure(ctx)
	}
	w := wire{Schema: "dill.snapshot.v1", Entrypoint: intent.Snapshot.Entrypoint, Files: make([]wireFile, 0, len(intent.Snapshot.Files))}
	for _, f := range intent.Snapshot.Files {
		data := f.Content
		if data == nil {
			data = []byte{}
		}
		w.Files = append(w.Files, wireFile{f.Path, data, len(data), hex.EncodeToString(f.SHA256[:])})
	}
	body, err := json.Marshal(w)
	if err != nil || len(body) > 24<<20 {
		return "", failure(ctx)
	}
	var client stagingClient
	if s.ssh != nil {
		client, err = s.openSSH(ctx, *s.ssh)
	} else {
		client, err = dillserve.NewLocal(s.target)
	}
	if err != nil {
		return "", failure(ctx)
	}
	defer client.Close()
	// Status performs its own health negotiation. Check staging capability after
	// it, so the latest observed advertisement is the one gating submission.
	if _, err = client.Status(ctx); err != nil {
		return "", failure(ctx)
	}
	h, err := client.Health(ctx)
	if err != nil {
		return "", failure(ctx)
	}
	found := false
	for _, cap := range h.Capabilities {
		if cap == "snapshot-staging" {
			found = true
		}
	}
	if !found {
		return "", ErrUnsupported
	}
	if acquire != nil {
		var release func()
		ctx, release, err = acquire(ctx)
		if err != nil || ctx == nil || release == nil {
			return "", ErrInvalid
		}
		defer release()
	}
	return client.Stage(ctx, body)
}
