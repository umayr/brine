//go:build unix

package dill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/secretbindings"
)

// This test is opt-in and read-only: the caller provisions a disposable engine,
// synthetic policy, matched candidate schema/config and external host sources.
func TestCandidateDillSecretReadOnlyIntegration(t *testing.T) {
	binary := os.Getenv("BRINE_DILL_CANDIDATE")
	if binary == "" {
		t.Skip("BRINE_DILL_CANDIDATE not set; paired candidate integration pending")
	}
	checksum := os.Getenv("BRINE_DILL_CANDIDATE_SHA256")
	commit := os.Getenv("BRINE_DILL_CANDIDATE_COMMIT")
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(checksum) {
		t.Fatal("candidate commit and checksum must be exact lowercase hex")
	}
	b, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("candidate binary unavailable")
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != checksum {
		t.Fatal("candidate checksum mismatch")
	}
	get := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Fatalf("missing %s", k)
		}
		return v
	}
	config := get("BRINE_DILL_CONFIG")
	policy := get("BRINE_DILL_POLICY")
	socket := get("BRINE_DILL_SOCKET")
	target := get("BRINE_DILL_TARGET")
	stack := get("BRINE_DILL_STACK")
	engine := get("BRINE_DILL_ENGINE")
	bindingPath := get("BRINE_DILL_BINDINGS")
	pkl := get("BRINE_DILL_PKL")
	schema := get("BRINE_DILL_SCHEMA")
	repo := get("BRINE_DILL_CANDIDATE_REPO")
	for _, p := range []string{binary, config, policy, socket, bindingPath, pkl, schema, repo} {
		if !filepath.IsAbs(p) {
			t.Fatal("candidate fixture paths must be absolute")
		}
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != commit {
		t.Fatal("candidate repository commit mismatch")
	}
	trackedChanges, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil || len(bytes.TrimSpace(trackedChanges)) != 0 {
		t.Fatal("candidate repository has tracked changes")
	}
	committedSchema, err := exec.Command("git", "-C", repo, "show", commit+":dill.pkl").Output()
	if err != nil {
		t.Fatal("candidate committed schema unavailable")
	}
	schemaBytes, err := os.ReadFile(schema)
	if err != nil || !bytes.Equal(schemaBytes, committedSchema) {
		t.Fatal("schema does not match candidate commit")
	}
	configBytes, err := os.ReadFile(config)
	if err != nil || !bytes.Contains(configBytes, []byte("file://"+schema)) {
		t.Fatal("fixture must explicitly import the candidate schema file URI")
	}
	binding, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal("binding fixture unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-candidate-test-")
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
	id := secretbindings.DeploymentID(stack)
	if err = state.SaveDraft(id, stack, target, binding); err != nil {
		t.Fatal("invalid binding fixture")
	}
	runner := New(binary)
	s := &SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: engine, Socket: socket, Policy: policy, Target: target, PklBinary: pkl}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = s.CheckDraft(ctx, id, stack, config); err != nil {
		candidateCheckDiagnostic(t, ctx, s, id, stack, config)
		t.Fatal("candidate status failed")
	}
	if _, err = s.Preview(ctx, id, stack, config); err != nil {
		t.Fatal("candidate preview failed")
	}
}

// candidateCheckDiagnostic emits only bounded phase results from synthetic CI
// fixtures; it never prints subprocess output, paths, bindings or proofs.
func candidateCheckDiagnostic(t *testing.T, ctx context.Context, s *SecretRunner, id string, stack, config string) {
	t.Helper()
	if err := s.Runner.CheckSecretCapabilities(ctx, s.Engine); err != nil {
		t.Log("synthetic status diagnostic: capability=failed")
		return
	}
	binding, err := s.State.BindingPath(id, stack, s.Target, true)
	if err != nil {
		t.Log("synthetic status diagnostic: binding=failed")
		return
	}
	data, exit, err := s.capture(ctx, "-f", config, "-p", stack, "--engine", s.Engine, "--engine-socket", s.Socket, "--secret-target", s.Target, "secrets", "check", "--secret-bindings", binding, "--format", "json")
	if err != nil {
		t.Log("synthetic status diagnostic: subprocess=failed")
		return
	}
	m, parseErr := strictObject(data, "contract", "status")
	if parseErr != nil {
		t.Logf("synthetic status diagnostic: exit=%d response=invalid", exit)
		return
	}
	var contract, status string
	if json.Unmarshal(m["contract"], &contract) != nil || json.Unmarshal(m["status"], &status) != nil {
		t.Logf("synthetic status diagnostic: exit=%d response=invalid", exit)
		return
	}
	t.Logf("synthetic status diagnostic: exit=%d contract_match=%t available=%t", exit, contract == "dill.alpha-secrets/1", status == "available")
}
