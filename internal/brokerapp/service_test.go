package brokerapp

import (
	"encoding/json"
	"fmt"
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serviceFixture(t *testing.T) (string, string, secretbroker.Scope) {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(data), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(data); err != nil {
		t.Fatal(err)
	}
	scope := secretbroker.Scope{RegistrationID: strings.Repeat("a", 32), StackName: "demo", TargetRegistration: "local"}
	p := Policy{Version: 1, Providers: secrets.Config{Version: 1, Connections: map[string]secrets.ConnectionConfig{"files": {Files: &secrets.FilesConfig{Sources: map[string]secrets.FileRef{"password": {Path: "/operator/private-file"}}}}}}, Registrations: []Registration{{Scope: scope, Connections: []string{"files"}}}}
	raw, _ := json.Marshal(p)
	policy := filepath.Join(data, "policy.json")
	if err := os.WriteFile(policy, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return data, policy, scope
}
func TestServiceStartupPreservesProtectionWhenPolicyRemoved(t *testing.T) {
	data, policy, scope := serviceFixture(t)
	s, err := Open(data, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() || len(s.Connections()) != 1 || strings.Contains(fmt.Sprintf("%#v", s), "private-file") {
		t.Fatal("policy not private/configured")
	}
	if _, err = s.Save(scope, 0, "admin", []secretbroker.Binding{{Name: "password", ConnectionID: "files", Kind: secretbroker.ProviderLocal, LocalAlias: "password"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() || s.RequireUnprotected("demo") == nil {
		t.Fatal("policy removal unprotected stack")
	}
	s.Close()
	if err = Initialize(data); err == nil {
		t.Fatal("reinitialized existing state")
	}
	if err = os.Remove(statePath(data)); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(data, ""); err == nil {
		t.Fatal("missing used DB recreated or ignored")
	}
	if err = os.RemoveAll(filepath.Dir(statePath(data))); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(data, ""); err == nil {
		t.Fatal("deleted broker directory bypassed permanent protection marker")
	}
}
func TestServiceInvalidPolicyCannotEnableProviders(t *testing.T) {
	data, policy, _ := serviceFixture(t)
	if err := os.Chmod(policy, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(data, policy); err == nil {
		t.Fatal("public policy accepted")
	}
	if _, err := Open(t.TempDir(), policy); err == nil {
		t.Fatal("missing state auto-provisioned")
	}
}
