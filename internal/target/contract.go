// Package target describes Brine's configured local execution boundary.
// Configuration is not an attestation, capability negotiation or readiness check.
package target

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	Legacy = "legacy"
	Alpha  = "alpha"
)

var ErrNegotiationUnavailable = errors.New("Alpha mutations unavailable: released Dill v0.7.0 has no target/ownership execution contract; pinned stack stop/down cleanup remains available")

// ValidateProfile deliberately preserves ambient/default compatibility only in legacy.
func ValidateProfile(profile, engine, socket string) error {
	if profile == "" || profile == Legacy {
		return nil
	}
	if profile != Alpha {
		return errors.New("profile must be legacy or alpha")
	}
	if engine != "docker" && engine != "podman" {
		return errors.New("Alpha requires docker or podman")
	}
	socket = strings.TrimPrefix(socket, "unix://")
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return errors.New("Alpha requires an explicit absolute local engine socket")
	}
	return nil
}

// Identity reports selection, never host identity inferred from engine type.
type Identity struct {
	Profile string
	Engine  string
	Socket  string
	Status  string
}

func Configured(profile, engine, socket string) Identity {
	if profile == "" {
		profile = Legacy
	}
	socket = strings.TrimPrefix(socket, "unix://")
	if filepath.IsAbs(socket) {
		socket = "unix://" + socket
	}
	if socket == "" {
		socket = "not configured"
	}
	return Identity{profile, engine, socket, "Configured only; target negotiation and readiness not established"}
}
