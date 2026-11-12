//go:build unix

package dill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
)

func TestSecretRunnerRejectsOldBinaryAndDriftWithoutFreshProof(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-secret-negative-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "state")
	workspace := filepath.Join(dir, "workspace")
	for _, p := range []string{root, workspace} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state, err := secretbindings.Open(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = state.SaveDraft("demo", "demo", "lab-a", []byte(testBinding)); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "dill")
	r := SecretRunner{Runner: New(binary), State: state, Coordinator: &secretbindings.Coordinator{}, Engine: "docker", Socket: "/tmp/socket", Policy: "/tmp/policy", Target: "lab-a"}
	config := filepath.Join(workspace, "compose.pkl")
	script := `#!/bin/sh
echo synthetic-secret-canary >&2
exit 1
`
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Preview(context.Background(), "demo", "demo", config); err != ErrSecretCapabilities {
		t.Fatalf("old binary: %v", err)
	}
	strict := &StrictController{Secret: &r, Resolve: func(string) (string, error) { return "demo", nil }, LegacyGate: func(context.Context, string) error { return nil }}
	if err = strict.Up(context.Background(), config); err != ErrSecretCapabilities {
		t.Fatalf("unmarked up bypassed discovery: %v", err)
	}
	if err = strict.Start(context.Background(), config, false); err != ErrSecretCapabilities {
		t.Fatalf("start bypassed discovery: %v", err)
	}
	if _, err = strict.Plan(context.Background(), config); err != ErrSecretCapabilities {
		t.Fatalf("plan bypassed discovery: %v", err)
	}
	script = `#!/bin/sh
case " $* " in
 *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}';exit 0;;
 *" plan "*) echo '{"changes":[{"service":"web","kind":"create","diffs":[]}],"summary":{"create":1,"recreate":0,"remove":0,"noop":0}}';echo synthetic-secret-canary >&2;exit 1;;
esac
exit 2
`
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = r.Preview(context.Background(), "demo", "demo", config)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-canary") {
		t.Fatalf("missing proof accepted or leaked: %v", err)
	}
	if _, err = state.Preview("demo", "demo", "lab-a"); err == nil {
		t.Fatal("preview retained without proof")
	}
}
