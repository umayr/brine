// Package secrets acquires individual static secrets from operator-configured
// connections. Callers must authorize the binding and its connection alias
// before acquisition. This package does not expose routes or persist values.
//
// Each acquisition has a ten-second budget, with limits of 1 MiB per HTTP
// response, 8 KiB per credential file and 64 KiB per value. Remote providers
// accept string values only. Local values are returned as exact bytes.
package secrets

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	SchemaVersion = 1
	MaxValueBytes = 64 << 10

	maxResponseBytes   = 1 << 20
	maxCredentialBytes = 8 << 10
	requestTimeout     = 10 * time.Second
	maxEntries         = 256 // connections, or sources per file connection
)

// Error is a fixed, safe-to-report classification. Provider diagnostics,
// URLs, filesystem paths, credentials and values are never attached.
type Error uint8

const (
	ErrInvalidConfig Error = iota + 1
	ErrInvalidSelector
	ErrDenied
	ErrNotFound
	ErrVersionMismatch
	ErrTooLarge
	ErrUnavailable
	ErrInvalidResponse
	ErrCanceled
	ErrDeadline
	ErrValueSerialization
)

func (e Error) Error() string {
	switch e {
	case ErrInvalidConfig:
		return "invalid secret provider configuration"
	case ErrInvalidSelector:
		return "invalid secret selector"
	case ErrDenied:
		return "secret acquisition denied"
	case ErrNotFound:
		return "secret not found"
	case ErrVersionMismatch:
		return "secret version mismatch"
	case ErrTooLarge:
		return "secret acquisition size limit exceeded"
	case ErrUnavailable:
		return "secret provider unavailable"
	case ErrInvalidResponse:
		return "invalid secret provider response"
	case ErrCanceled:
		return "secret acquisition canceled"
	case ErrDeadline:
		return "secret acquisition deadline exceeded"
	case ErrValueSerialization:
		return "secret value cannot be serialized"
	default:
		return "secret acquisition failed"
	}
}

// Value holds plaintext in memory only. Bytes explicitly returns a copy;
// callers are responsible for controlling the lifetime and use of that copy.
// This is not a memory erasure guarantee.
type Value struct {
	// A closure also keeps the bytes out of recursive fmt reflection when a
	// Value is embedded in an unexported field in another type.
	reveal   func() []byte
	release  func()
	revision string
}

const redacted = "[REDACTED]"

// NewValue constructs a bounded detached value for compiled-in adapters.
func NewValue(data []byte) (Value, error) {
	if len(data) > MaxValueBytes {
		return Value{}, ErrTooLarge
	}
	return newValue(append([]byte(nil), data...)), nil
}

func newValue(data []byte) Value {
	var mu sync.Mutex
	return Value{reveal: func() []byte { mu.Lock(); defer mu.Unlock(); return append([]byte{}, data...) }, release: func() { mu.Lock(); defer mu.Unlock(); clear(data); data = nil }}
}

// Close releases this value and all copies of it. It is idempotent and safe
// against concurrent Bytes calls. Copies returned earlier remain caller-owned.
// Clearing is best-effort lifetime reduction, not a memory erasure guarantee.
func (v Value) Close() {
	if v.release != nil {
		v.release()
	}
}

// Revision is provider-reported metadata; empty means unavailable, never a value hash.
func (v Value) Revision() string { return v.revision }

func (v Value) Bytes() []byte {
	if v.reveal == nil {
		return nil
	}
	return v.reveal()
}

func (Value) String() string   { return redacted }
func (Value) GoString() string { return redacted }
func (Value) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte(redacted))
}
func (Value) MarshalJSON() ([]byte, error) { return nil, ErrValueSerialization }

type Kind string

const (
	KindVault     Kind = "vault-kv-v2"
	KindInfisical Kind = "infisical-static"
	KindFile      Kind = "file"
)

type Capabilities struct {
	Kind       Kind
	VersionPin bool
}

// Config and its connection details are operator input, never binding input.
// Exactly one provider configuration must be set for each connection.
// On platforms without the secure file reader (currently non-Linux), file
// acquisition and file-backed authentication fail closed.
type Config struct {
	Version     uint
	Connections map[string]ConnectionConfig
}

type ConnectionConfig struct {
	Vault     *VaultConfig
	Infisical *InfisicalConfig
	Files     *FilesConfig
}

// FileRef is a canonical absolute Linux path. Its components must not be
// symlinks, and the file must have one link, mode 0400 or 0600, and be owned by
// root or the effective UID. Credential files contain a single printable
// ASCII atom with an optional final LF or CRLF.
type FileRef struct{ Path string }

type VaultConfig struct {
	Endpoint   string // HTTPS origin, without an API path
	Mount      string // KV v2 mount
	PathPrefix string // mandatory scope within the mount
	TokenFile  FileRef
}

type InfisicalConfig struct {
	Endpoint         string // HTTPS origin, without an API path
	ProjectID        string
	Environment      string
	Folder           string // absolute Infisical folder, e.g. /approved
	ClientIDFile     FileRef
	ClientSecretFile FileRef
}

type FilesConfig struct{ Sources map[string]FileRef }

// Selector is a binding reference, not a connection configuration. Exactly
// one provider selector must be set, and it must match the connection kind.
// Revision 0 selects the current version; a positive revision must match the
// version reported by the provider. File references do not support pinning.
type Selector struct {
	Version    uint
	Connection string
	Vault      *VaultSelector
	Infisical  *InfisicalSelector
	File       *FileSelector
}

type VaultSelector struct {
	Path     string // relative to the configured prefix
	Key      string
	Revision uint64
}

type InfisicalSelector struct {
	Folder   string // optional absolute folder within the configured folder scope
	Key      string
	Revision uint64
}

type FileSelector struct{ Source string }

type provider interface {
	capabilities() Capabilities
	acquire(context.Context, Selector) (Value, error)
}

// Registry snapshots configuration and is safe for concurrent acquisition.
// It does not cache file contents, HTTP responses or authentication tokens.
type Registry struct{ providers map[string]provider }

func New(cfg Config) (*Registry, error) { return newRegistry(cfg, nil) }

// testTransport is only for package tests. Public construction always uses
// verified HTTPS and never allows transport injection.
func newRegistry(cfg Config, testTransport http.RoundTripper) (*Registry, error) {
	if cfg.Version != SchemaVersion || len(cfg.Connections) > maxEntries {
		return nil, ErrInvalidConfig
	}
	client := newHTTPClient(testTransport)
	r := &Registry{providers: make(map[string]provider, len(cfg.Connections))}
	for name, connection := range cfg.Connections {
		if !validName(name) || !exactlyOne(connection.Vault != nil, connection.Infisical != nil, connection.Files != nil) {
			return nil, ErrInvalidConfig
		}
		switch {
		case connection.Vault != nil:
			c := *connection.Vault
			origin, ok := parseOrigin(c.Endpoint, testTransport != nil)
			if !ok || !validRelativePath(c.Mount) || !validRelativePath(c.PathPrefix) || !validFileRef(c.TokenFile) {
				return nil, ErrInvalidConfig
			}
			r.providers[name] = vaultProvider{cfg: c, origin: origin, client: client}
		case connection.Infisical != nil:
			c := *connection.Infisical
			origin, ok := parseOrigin(c.Endpoint, testTransport != nil)
			if !ok || !validName(c.ProjectID) || !validName(c.Environment) || !validFolder(c.Folder) ||
				!validFileRef(c.ClientIDFile) || !validFileRef(c.ClientSecretFile) {
				return nil, ErrInvalidConfig
			}
			r.providers[name] = infisicalProvider{cfg: c, origin: origin, client: client}
		case connection.Files != nil:
			sources := connection.Files.Sources
			if len(sources) == 0 || len(sources) > maxEntries {
				return nil, ErrInvalidConfig
			}
			p := make(fileProvider, len(sources))
			for alias, ref := range sources {
				if !validName(alias) || !validFileRef(ref) {
					return nil, ErrInvalidConfig
				}
				p[alias] = ref
			}
			r.providers[name] = p
		}
	}
	return r, nil
}

func (r *Registry) Capabilities(connection string) (Capabilities, error) {
	p, err := r.lookup(connection)
	if err != nil {
		return Capabilities{}, err
	}
	return p.capabilities(), nil
}

func (r *Registry) lookup(connection string) (provider, error) {
	if !validName(connection) {
		return nil, ErrInvalidSelector
	}
	if r != nil {
		if p := r.providers[connection]; p != nil {
			return p, nil
		}
	}
	return nil, ErrDenied
}

// Acquire performs one bounded acquisition. The deadline covers the complete
// request, including login if required. File I/O assumes trusted local Linux
// filesystems: context is checked around syscalls, not used to interrupt them.
func (r *Registry) Acquire(ctx context.Context, s Selector) (Value, error) {
	if ctx == nil || !validSelector(s) {
		return Value{}, ErrInvalidSelector
	}
	p, err := r.lookup(s.Connection)
	if err != nil {
		return Value{}, err
	}
	kind := p.capabilities().Kind
	if (s.File != nil && kind != KindFile) || (s.Vault != nil && kind != KindVault) ||
		(s.Infisical != nil && kind != KindInfisical) {
		return Value{}, ErrInvalidSelector
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := contextError(ctx); err != nil {
		return Value{}, err
	}
	v, err := p.acquire(ctx, s)
	if canceled := contextError(ctx); canceled != nil {
		return Value{}, canceled
	}
	return v, err
}

func contextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.Canceled:
		return ErrCanceled
	case context.DeadlineExceeded:
		return ErrDeadline
	default:
		return nil
	}
}

type fileProvider map[string]FileRef

func (fileProvider) capabilities() Capabilities { return Capabilities{Kind: KindFile} }

func (p fileProvider) acquire(ctx context.Context, s Selector) (Value, error) {
	ref, ok := p[s.File.Source]
	if !ok {
		return Value{}, ErrDenied
	}
	data, err := readProtectedFile(ctx, ref, MaxValueBytes)
	if err != nil {
		return Value{}, err
	}
	return newValue(data), nil
}

// Unsupported platforms must never fall back to following filesystem links.
var readProtectedFile = func(context.Context, FileRef, int) ([]byte, error) {
	return nil, ErrUnavailable
}
