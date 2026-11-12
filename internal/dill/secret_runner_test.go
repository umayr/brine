//go:build unix

package dill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
)

const testBinding = `{"version":"dill.secret-bindings/1","target":"lab-a","stack":"demo","bindings":[{"name":"db","source":"db-prod","revision":"r1"}]}`

func TestSecretRunnerPreviewAndApply(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-runner-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	private := filepath.Join(dir, "state")
	workspace := filepath.Join(dir, "workspace")
	for _, d := range []string{private, workspace} {
		if err = os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id := secretbindings.DeploymentID("demo")
	if err = st.SaveDraft(id, "demo", "lab-a", []byte(testBinding)); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "dill")
	log := filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
if [ -n "$BRINE_SYNTHETIC_SECRET" ] || [ -n "$DILL_SECRET_TARGET" ]; then exit 9; fi
case " $* " in *" secret-capabilities "*) ;; *) case " $* " in *" -p demo --engine docker --engine-socket /tmp/synthetic.sock --secret-target lab-a "*) ;; *) exit 9;; esac;; esac
case " $* " in
 *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file","podman-host-file"]}'; exit 0;;
 *" plan "*)
  while [ "$#" -gt 0 ]; do if [ "$1" = "--proof-file" ]; then shift; proof="$1"; fi; shift; done
  if [ -n "$proof" ]; then printf '%%s' '{"contract":"dill.alpha-secrets-preview/1","proof":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}' > "$proof"; chmod 600 "$proof"; fi
  echo '{"changes":[{"service":"web","kind":"create","diffs":[]}],"summary":{"create":1,"recreate":0,"remove":0,"noop":0}}';exit 1;;
 *" up "*) echo "called $*" >> '%s'; exit 0;;
 *" secrets check "*) echo '{"contract":"dill.alpha-secrets/1","status":"available"}'; exit 0;;
 *" validate "*) echo '{"valid":true}'; exit 0;;
esac
exit 2
`, log)
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRINE_SYNTHETIC_SECRET", "synthetic-canary")
	s := SecretRunner{Runner: New(bin), State: st, Coordinator: &secretbindings.Coordinator{}, Engine: "docker", Socket: "/tmp/synthetic.sock", Policy: "/tmp/synthetic-policy", Target: "lab-a"}
	config := filepath.Join(workspace, "compose.pkl")
	ctx := context.Background()
	ordinary := &StrictController{Secret: &s, Resolve: func(string) (string, error) { return "demo", nil }, LegacyGate: func(context.Context, string) error { return nil }}
	if err = ordinary.Validate(ctx, config, nil); err != nil {
		t.Fatalf("unmarked strict validation: %v", err)
	}
	if _, err = ordinary.Plan(ctx, config); err != nil {
		t.Fatalf("unmarked strict plan: %v", err)
	}
	if err = s.ApplyActive(ctx, id, "demo", config); err == nil {
		t.Fatal("unapproved draft applied")
	}
	if err = s.CheckDraft(ctx, id, "demo", config); err != nil {
		t.Fatalf("draft status: %v", err)
	}
	if err = s.CheckActive(ctx, id, "demo", config); err == nil {
		t.Fatal("unapproved active status succeeded")
	}
	p, err := s.Preview(ctx, id, "demo", config)
	if err != nil || p.Summary.Create != 1 {
		t.Fatalf("preview: %v", err)
	}
	if _, err = os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("preview applied")
	}
	snapshot, err := st.Preview(id, "demo", "lab-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmAndApply(ctx, id, "demo", config, snapshot.Token); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckActive(ctx, id, "demo", config); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(log); err != nil || strings.Count(string(b), "called") != 1 {
		t.Fatal("apply missing")
	}
	if err = s.ApplyActive(ctx, id, "demo", config); err != nil {
		t.Fatal(err)
	}
	controller := &StrictController{Secret: &s, Resolve: func(string) (string, error) { return "demo", nil }, LegacyGate: func(context.Context, string) error { return nil }}
	if err = controller.Up(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err = controller.Start(ctx, config, true); err == nil {
		t.Fatal("marked deployment restart permitted")
	}
	if _, err = controller.Plan(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err = controller.Validate(ctx, config, nil); err != nil {
		t.Fatalf("strict validation: %v", err)
	}
	b, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(b), "draft.json") || strings.Count(string(b), "active.json") != 3 {
		t.Fatal("deployment did not exclusively use active bindings")
	}
	newer := strings.Replace(testBinding, `"revision":"r1"`, `"revision":"r2"`, 1)
	if err = st.SaveDraft(id, "demo", "lab-a", []byte(newer)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Preview(ctx, id, "demo", config); err != nil {
		t.Fatal(err)
	}
	failedScript := strings.Replace(script, fmt.Sprintf(`echo "called $*" >> '%s'; exit 0;;`, log), fmt.Sprintf(`echo "called $*" >> '%s'; exit 1;;`, log), 1)
	if failedScript == script {
		t.Fatal("failed to configure synthetic apply failure")
	}
	if err = os.WriteFile(bin, []byte(failedScript), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot, err = st.Preview(id, "demo", "lab-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmAndApply(ctx, id, "demo", config, snapshot.Token); err != ErrSecretApplyFailed {
		t.Fatalf("failed apply: %v", err)
	}
	active, err := st.Active(id, "demo", "lab-a")
	if err != nil || !strings.Contains(string(active), `"revision":"r2"`) {
		t.Fatal("approved binding not retained after apply failure")
	}
}
func TestSecretPlanAndProofStrictness(t *testing.T) {
	proof := `{"contract":"dill.alpha-secrets-preview/1","proof":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}`
	for _, v := range []string{proof + `{}`, strings.Replace(proof, `"proof":`, `"proof":null,"proof":`, 1), strings.Replace(proof, `AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE`, `short`, 1)} {
		if _, err := parseProof([]byte(v)); err == nil {
			t.Fatal("invalid proof accepted")
		}
	}
	for _, v := range []string{`{}`, `{"changes":[],"summary":null}`, `{"changes":[],"summary":{}} trailing`, `{"changes":[],"summary":{}}`} {
		if _, err := parseSecretPlan([]byte(v), 1); err == nil {
			t.Fatal("invalid plan accepted")
		}
		if _, err := parseSecretPlan([]byte(v), 0); err == nil {
			t.Fatal("invalid no-drift plan accepted")
		}
	}
}
