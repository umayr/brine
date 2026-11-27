// Package brokerapp owns application policy, references and reviewed dispatch.
// Provider values never enter application configuration or durable history.
package brokerapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

var ErrUnavailable = errors.New("secret broker unavailable; inspect operator configuration and protected state")

type Registration struct {
	Scope       secretbroker.Scope `json:"scope"`
	Connections []string           `json:"connections"`
}
type Policy struct {
	Version       int                     `json:"version"`
	Targets       map[string]TargetPolicy `json:"targets,omitempty"`
	Providers     secrets.Config          `json:"providers"`
	Registrations []Registration          `json:"registrations"`
}
type Connection struct{ ID, Kind, Endpoint, Scope string }
type Service struct {
	State    *secretbroker.Store
	Manager  *secretbroker.Manager
	registry *secrets.Registry
	policy   Policy
	revision string
}

func statePath(data string) string { return filepath.Join(data, "broker-state", "references.db") }
func Initialize(data string) error {
	root := filepath.Dir(statePath(data))
	if !filepath.IsAbs(data) || filepath.Clean(data) != data {
		return ErrUnavailable
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		return ErrUnavailable
	}
	anchor, err := os.OpenFile(filepath.Join(data, "broker-protected"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrUnavailable
	}
	if _, err = anchor.Write([]byte("brine-broker-state/2\n")); err != nil {
		anchor.Close()
		return ErrUnavailable
	}
	if errors.Join(anchor.Sync(), anchor.Close()) != nil {
		return ErrUnavailable
	}
	parent, err := os.Open(data)
	if err != nil {
		return ErrUnavailable
	}
	syncErr := parent.Sync()
	parent.Close()
	if syncErr != nil || os.Mkdir(root, 0700) != nil {
		return ErrUnavailable
	}
	if secretbroker.Provision(statePath(data)) != nil {
		return ErrUnavailable
	}
	return nil
}
func loadPolicy(path string) (Policy, string, error) {
	var p Policy
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return p, "", ErrUnavailable
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return p, "", ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return p, "", ErrUnavailable
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return p, "", ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, brokerwire.MaxMetadata+1))
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(raw), &p) != nil || p.Version != 1 || len(p.Registrations) > 1024 {
		return p, "", ErrUnavailable
	}
	return p, brokerwire.Digest(raw), nil
}

// Open always probes the fixed protected-state location, even without a policy.
// Removing flags cannot restore ordinary execution or recreate missing state.
func Open(data, policyFile string) (*Service, error) {
	path := statePath(data)
	_, err := os.Lstat(filepath.Dir(path))
	anchor, anchorErr := os.Lstat(filepath.Join(data, "broker-protected"))
	if os.IsNotExist(err) && os.IsNotExist(anchorErr) && policyFile == "" {
		return nil, nil
	}
	if err != nil || anchorErr != nil || !anchor.Mode().IsRegular() || anchor.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	state, err := secretbroker.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	s := &Service{State: state}
	fail := func() (*Service, error) { state.Close(); return nil, ErrUnavailable }
	if policyFile == "" {
		return s, nil
	}
	p, revision, err := loadPolicy(policyFile)
	if err != nil {
		return fail()
	}
	registry, err := secrets.New(p.Providers)
	if err != nil {
		return fail()
	}
	seen := map[string]bool{}
	for _, r := range p.Registrations {
		if seen[r.Scope.RegistrationID] || len(r.Connections) == 0 {
			return fail()
		}
		seen[r.Scope.RegistrationID] = true
		allowed := map[string]bool{}
		for _, id := range r.Connections {
			if allowed[id] {
				return fail()
			}
			allowed[id] = true
			if _, err := registry.Capabilities(id); err != nil {
				return fail()
			}
		}
		if _, err := state.Load(r.Scope); err == secretbroker.ErrUnregistered {
			if state.Register(r.Scope, "operator") != nil {
				return fail()
			}
		} else if err != nil {
			return fail()
		}
	}
	s.policy = p
	s.revision = revision
	s.registry = registry
	s.Manager, err = secretbroker.NewManager(state, registry, func(ctx context.Context, i secretbroker.ReviewIdentity, b []secretbroker.Binding) error {
		if ctx.Err() != nil || i.PolicyRevision != s.revision {
			return ErrUnavailable
		}
		return s.Allowed(i.Scope, b)
	})
	if err != nil {
		return fail()
	}
	return s, nil
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	if s.Manager != nil {
		s.Manager.Close()
	}
	s.State.Close()
}
func (s *Service) Enabled() bool { return s != nil && s.Manager != nil }
func (s *Service) Revision() string {
	if s == nil {
		return ""
	}
	return s.revision
}
func (s *Service) Allowed(scope secretbroker.Scope, b []secretbroker.Binding) error {
	if !s.Enabled() {
		return ErrUnavailable
	}
	for _, r := range s.policy.Registrations {
		if r.Scope != scope {
			continue
		}
		allowed := map[string]bool{}
		for _, id := range r.Connections {
			allowed[id] = true
		}
		for _, binding := range b {
			if !allowed[binding.ConnectionID] {
				return ErrUnavailable
			}
			caps, err := s.registry.Capabilities(binding.ConnectionID)
			if err != nil {
				return ErrUnavailable
			}
			if (binding.Kind == secretbroker.ProviderVault && caps.Kind != secrets.KindVault) || (binding.Kind == secretbroker.ProviderInfisical && caps.Kind != secrets.KindInfisical) || (binding.Kind == secretbroker.ProviderLocal && caps.Kind != secrets.KindFile) {
				return ErrUnavailable
			}
		}
		return nil
	}
	return ErrUnavailable
}
func (s *Service) Connections() []Connection {
	if !s.Enabled() {
		return nil
	}
	out := []Connection{}
	for id, c := range s.policy.Providers.Connections {
		row := Connection{ID: id}
		switch {
		case c.Vault != nil:
			row.Kind = "Vault KV v2"
			row.Endpoint = c.Vault.Endpoint
			row.Scope = c.Vault.Mount + "/" + c.Vault.PathPrefix
		case c.Infisical != nil:
			row.Kind = "Infisical"
			row.Endpoint = c.Infisical.Endpoint
			row.Scope = c.Infisical.ProjectID + " / " + c.Infisical.Environment + " / " + c.Infisical.Folder
		case c.Files != nil:
			row.Kind = "Local files"
			row.Scope = "Allowlisted sources"
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Service) Scopes(stack string) []secretbroker.Scope {
	out := []secretbroker.Scope{}
	if !s.Enabled() {
		return out
	}
	for _, r := range s.policy.Registrations {
		if r.Scope.StackName == stack {
			out = append(out, r.Scope)
		}
	}
	return out
}
func (s *Service) Save(scope secretbroker.Scope, expected int64, actor string, b []secretbroker.Binding) (int64, error) {
	if s.Allowed(scope, b) != nil {
		return 0, ErrUnavailable
	}
	return s.State.Save(scope, expected, actor, b)
}
func (s *Service) Protected(stack string) (bool, error) {
	if s == nil {
		return false, nil
	}
	return s.State.StackProtected(stack)
}
func (s *Service) RequireUnprotected(stack string) error {
	protected, err := s.Protected(stack)
	if err != nil || protected {
		return ErrUnavailable
	}
	return nil
}

func (*Service) Format(w fmt.State, _ rune)   { _, _ = io.WriteString(w, "BrokerService{redacted}") }
func (*Service) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }
