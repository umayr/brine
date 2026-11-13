//go:build unix

package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
)

// Strict diagnostics are discarded, not heuristically scrubbed. Valid structured
// plans may deliberately contain deployment values; cleanup streams remain
// authenticated operator diagnostics even with a strict controller installed.
func TestDiagnosticSentinelStrictResultAndLifecycleMatrix(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-diagnostic-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	private, workspace := filepath.Join(dir, "private"), filepath.Join(dir, "workspace")
	for _, p := range []string{private, workspace} {
		if err = os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	const login = "synthetic-forbidden-login"
	const token = "synthetic-forbidden-api"
	const plaintext = "synthetic-forbidden-plaintext"
	const diagnostic = "synthetic-deployment-strict-diagnostic"
	t.Setenv("BRINE_AUTH_PASSWORD", login)
	t.Setenv("BRINE_API_TOKEN", token)
	t.Setenv("BRINE_SYNTHETIC_PLAINTEXT", plaintext)
	mode, calls, homes, env := filepath.Join(dir, "mode"), filepath.Join(dir, "calls"), filepath.Join(dir, "homes"), filepath.Join(dir, "env")
	bin := filepath.Join(dir, "dill")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
printf '%%s\n' "$HOME" >> %q
printf '%%s\n' "$BRINE_AUTH_PASSWORD" "$BRINE_API_TOKEN" "$BRINE_SYNTHETIC_PLAINTEXT" >> %q
case " $* " in *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}';exit 0;; esac
mode=$(cat %q)
printf 'synthetic-deployment-strict-diagnostic-stderr\n' >&2
case "$mode" in
 failure) echo 'synthetic-deployment-strict-diagnostic-stdout'; exit 2;;
 malformed) echo '{"valid":false,"error":"synthetic-deployment-strict-diagnostic"}'; exit 0;;
 input) cat >&2; echo '{"valid":false}'; exit 0;;
esac
case " $* " in
 *" validate "*) echo '{"valid":true}';exit 0;;
 *" plan "*)
  while [ "$#" -gt 0 ]; do if [ "$1" = '--proof-file' ];then shift;proof="$1";fi;shift;done
  if [ -n "$proof" ];then echo '{"contract":"dill.alpha-secrets-preview/1","proof":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}' > "$proof";chmod 600 "$proof";fi
  echo '{"changes":[{"service":"web","kind":"create","diffs":[{"field":"deployment","before":"","after":"synthetic-deployment-strict-diagnostic"}]}],"summary":{"create":1,"recreate":0,"remove":0,"noop":0}}';exit 1;;
esac
printf 'synthetic-deployment-strict-diagnostic-stdout\n'
exit 0
`, calls, homes, env, mode)
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(bin)
	r.Engine, r.Socket = "docker", "/tmp/synthetic.sock"
	r.ResolveStack = func(string) (string, error) { return "demo", nil }
	s := &SecretRunner{Runner: r, State: state, Coordinator: &r.Coordinator, Engine: "docker", Socket: r.Socket, Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	c := &StrictController{Secret: s, Resolve: r.ResolveStack, LegacyGate: func(context.Context, string) error { return nil }}
	r.StrictActions = c
	config := filepath.Join(workspace, "compose.pkl")
	setMode := func(m string) {
		t.Helper()
		if err := os.WriteFile(mode, []byte(m), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, m := range []string{"valid", "malformed", "failure", "input"} {
		t.Run("strict-validate-"+m, func(t *testing.T) {
			setMode(m)
			err := r.ValidateContent(ctx, config, plaintext)
			if m == "valid" {
				if err != nil {
					t.Fatal("valid result rejected")
				}
			} else if err != ErrSecretUnavailable {
				t.Fatal("strict validation did not return safe error")
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("strict validation leaked diagnostic")
			}
		})
	}
	setMode("failure")
	if p, err := r.Plan(ctx, config); err != ErrSecretUnavailable || p != nil {
		t.Fatal("malformed strict stdout was returned")
	}
	for _, action := range []struct {
		name string
		run  func(context.Context, string, *bytes.Buffer) error
	}{
		{"up", func(ctx context.Context, p string, w *bytes.Buffer) error { return r.Up(ctx, p, w) }},
		{"start", func(ctx context.Context, p string, w *bytes.Buffer) error { return r.Start(ctx, p, w) }},
		{"restart", func(ctx context.Context, p string, w *bytes.Buffer) error { return r.Restart(ctx, p, w) }},
	} {
		t.Run("strict-"+action.name, func(t *testing.T) {
			var out bytes.Buffer
			err := action.run(ctx, config, &out)
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), diagnostic) {
				t.Fatal("strict lifecycle raw output leaked")
			}
		})
	}
	// Cleanup intentionally streams producer stdout + stderr, not a vault claim.
	for _, action := range []struct {
		name string
		run  func(*bytes.Buffer) error
	}{
		{"stop", func(w *bytes.Buffer) error { return r.Stop(ctx, config, w) }},
		{"down", func(w *bytes.Buffer) error { return r.Down(ctx, config, w) }},
		{"teardown", func(w *bytes.Buffer) error { return r.Teardown(ctx, config, false, w) }},
	} {
		t.Run("cleanup-"+action.name, func(t *testing.T) {
			var out bytes.Buffer
			err := action.run(&out)
			if err == nil || !strings.Contains(out.String(), diagnostic+"-stdout") || !strings.Contains(out.String(), diagnostic+"-stderr") {
				t.Fatal("cleanup producer streams not exercised")
			}
			for _, forbidden := range []string{login, token, plaintext} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatal("credential reached cleanup stream")
				}
			}
		})
	}
	setMode("valid")
	id := secretbindings.DeploymentID("demo")
	if err = state.SaveDraft(id, "demo", "lab-a", []byte(testBinding)); err != nil {
		t.Fatal(err)
	}
	p, err := s.Preview(ctx, id, "demo", config)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil || !bytes.Contains(b, []byte(diagnostic)) {
		t.Fatal("valid structured deployment value not exercised")
	}
	snapshot, err := state.Preview(id, "demo", "lab-a")
	if err != nil || !bytes.Contains(snapshot.Plan, []byte(diagnostic)) {
		t.Fatal("structured preview outcome not persisted")
	}
	// Active checked up succeeds but still suppresses its raw stdout/stderr.
	if err = s.ConfirmAndApply(ctx, id, "demo", config, snapshot.Token); err != nil {
		t.Fatal(err)
	}
	setMode("failure")
	if err = s.ApplyActive(ctx, id, "demo", config); err != ErrSecretApplyFailed {
		t.Fatal("checked up failure not safely reported")
	}
	b, err = os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{" validate ", " plan ", " up", " start", " restart", " stop", " down", " teardown"} {
		if !strings.Contains(string(b), action) {
			t.Fatal("sentinel producer action not executed")
		}
	}
	for _, path := range []string{calls, homes, env} {
		if _, err = os.Stat(path); err != nil {
			t.Fatal("producer witness missing")
		}
	}
	b, err = os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{login, token, plaintext} {
		if bytes.Contains(b, []byte(forbidden)) {
			t.Fatal("forbidden environment reached subprocess")
		}
	}
	b, err = os.ReadFile(homes)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(string(b)) {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("private subprocess home retained")
		}
	}
	err = filepath.WalkDir(private, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), "proof-") {
			t.Fatal("proof reservation retained")
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "proof.json" {
			t.Fatal("proof output retained")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for _, forbidden := range []string{login, token, plaintext} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Fatal("forbidden input persisted")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
