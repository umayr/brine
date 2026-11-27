// Package secretbroker stores reference-only binding state. It does not
// resolve references, manage credentials, or authorize callers.
package secretbroker

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Errors deliberately do not include paths, references, actors, or driver errors.
var (
	ErrInvalidInput       = errors.New("invalid binding state input")
	ErrUnsafePath         = errors.New("unsafe binding state path")
	ErrExists             = errors.New("binding state file already exists")
	ErrUnavailable        = errors.New("binding state unavailable or invalid")
	ErrRegistrationExists = errors.New("binding registration already exists")
	ErrScopeMismatch      = errors.New("binding registration scope mismatch")
	ErrUnregistered       = errors.New("binding scope is not registered")
	ErrConflict           = errors.New("binding revision conflict")
	ErrCapacity           = errors.New("binding state capacity reached")
	ErrRevisionNotFound   = errors.New("binding revision not found")
)

const (
	MaxBindingsPerRevision = 128
	MaxRevisionsPerScope   = 128 // Includes registration at revision zero.
	MaxRegistrations       = 1024
	MaxTotalRevisions      = 16384
	MaxTotalBindingRows    = 262144

	maxNameLength      = 128
	maxActorLength     = 128
	maxReferenceLength = 512
)

// Scope is pinned at registration. A registration ID cannot be reassigned,
// even if all its bindings are removed. The target is an opaque identifier.
type Scope struct {
	RegistrationID     string
	StackName          string
	TargetRegistration string
}

type ProviderKind string

const (
	ProviderVault     ProviderKind = "vault"
	ProviderInfisical ProviderKind = "infisical"
	ProviderLocal     ProviderKind = "local"
)

// VaultRef identifies a property at a relative Vault path. Version zero
// means no specific version was requested.
type VaultRef struct {
	Path     string
	Property string
	Version  int64
}

// InfisicalRef identifies a key in an absolute Infisical folder. Version zero
// means no specific version was requested.
type InfisicalRef struct {
	Folder  string
	Key     string
	Version int64
}

// Binding carries exactly one reference matching Kind. ConnectionID is an
// opaque identifier, not connection configuration or authentication material.
type Binding struct {
	Name         string
	ConnectionID string
	Kind         ProviderKind
	Vault        VaultRef
	Infisical    InfisicalRef
	LocalAlias   string
}

// Snapshot is a detached copy of a complete revision, in binding-name order.
// Revision zero is the registered, initially unbound state.
type Snapshot struct {
	Scope    Scope
	Revision int64
	Bindings []Binding
}

type EventKind string

const (
	EventRegister EventKind = "register"
	EventSave     EventKind = "save"
	EventUnbind   EventKind = "unbind"
)

// AuditEvent intentionally excludes binding names, provider references, and
// connection IDs. LoadRevision explicitly retrieves historical references.
type AuditEvent struct {
	Revision     int64
	Kind         EventKind
	Actor        string
	At           time.Time
	BindingCount int
}

func redact(w fmt.State, label string) { _, _ = io.WriteString(w, label+"{redacted}") }

func (Scope) Format(w fmt.State, _ rune)        { redact(w, "Scope") }
func (VaultRef) Format(w fmt.State, _ rune)     { redact(w, "VaultRef") }
func (InfisicalRef) Format(w fmt.State, _ rune) { redact(w, "InfisicalRef") }
func (Binding) Format(w fmt.State, _ rune)      { redact(w, "Binding") }
func (Snapshot) Format(w fmt.State, _ rune)     { redact(w, "Snapshot") }
func (AuditEvent) Format(w fmt.State, _ rune)   { redact(w, "AuditEvent") }

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Metadata uses bounded ASCII identifiers. In particular, an identifier is
// not a URI, free-form annotation, or credential field.
func token(s string, max int, punctuation string) bool {
	if len(s) == 0 || len(s) > max || s == "." || s == ".." {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(punctuation, c) >= 0) {
			return false
		}
	}
	return true
}

func validScope(s Scope) bool {
	return validID(s.RegistrationID) && token(s.StackName, maxNameLength, "_.-") &&
		token(s.TargetRegistration, maxNameLength, "_.-")
}

func validActor(actor string) bool { return token(actor, maxActorLength, "_.-@+:") }

func referencePath(s string, absolute bool) bool {
	if len(s) == 0 || len(s) > maxReferenceLength {
		return false
	}
	if absolute {
		if s[0] != '/' {
			return false
		}
		s = s[1:]
		if s == "" {
			return true
		}
	}
	for _, part := range strings.Split(s, "/") {
		if !token(part, maxReferenceLength, "_.-") {
			return false
		}
	}
	return true
}

func validBinding(b Binding) bool {
	if !token(b.Name, maxNameLength, "_.-") || !token(b.ConnectionID, maxNameLength, "_.-") {
		return false
	}
	switch b.Kind {
	case ProviderVault:
		return referencePath(b.Vault.Path, false) && token(b.Vault.Property, maxNameLength, "_.-") &&
			b.Vault.Version >= 0 && b.Infisical == (InfisicalRef{}) && b.LocalAlias == ""
	case ProviderInfisical:
		return referencePath(b.Infisical.Folder, true) && token(b.Infisical.Key, maxNameLength, "_.-") &&
			b.Infisical.Version >= 0 && b.Vault == (VaultRef{}) && b.LocalAlias == ""
	case ProviderLocal:
		return token(b.LocalAlias, maxNameLength, "_.-") && b.Vault == (VaultRef{}) && b.Infisical == (InfisicalRef{})
	default:
		return false
	}
}
