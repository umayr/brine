package main

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/secretbindings"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuarantineBinaryCLI(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	invoke := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, binary, append([]string{"--data-dir", root, "--engine=synthetic-invalid"}, args...)...).CombinedOutput()
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	id := secretbindings.DeploymentID("synthetic")
	token, err := q.Begin(id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := invoke("--process-quarantine=list")
	if err != nil || !strings.Contains(string(out), token) {
		t.Fatalf("list: %v %s", err, out)
	}
	recoverArgs := []string{"--process-quarantine=recover", "--quarantine-stack=synthetic", "--quarantine-token=" + token, "--quarantine-inspected", "--quarantine-same-host"}
	if out, err = invoke(recoverArgs...); err == nil {
		t.Fatalf("same boot accepted: %s", out)
	}
	if q.Check(id) == nil {
		t.Fatal("denial cleared intent")
	}
	// Synthetic prior-boot fixture exercises successful binary recovery without
	// rebooting the host or exposing a production boot-identity override.
	prefix := strings.Repeat("0", 32)
	if token[:32] == prefix {
		prefix = strings.Repeat("1", 32)
	}
	old := prefix + token[32:]
	ledger, err := json.Marshal(map[string]string{id: old})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "quarantine.json"), ledger, 0600); err != nil {
		t.Fatal(err)
	}
	recoverArgs[2] = "--quarantine-token=" + old
	if out, err = invoke(recoverArgs[:3]...); err == nil {
		t.Fatalf("missing confirmations accepted: %s", out)
	}
	if out, err = invoke(recoverArgs...); err != nil {
		t.Fatalf("synthetic previous boot: %v %s", err, out)
	}
	reopened, err := secretbindings.OpenQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Check(id); err != nil {
		t.Fatal(err)
	}
	t.Log("built brine binary: list, same-boot denial, confirmation denial, synthetic prior-boot recovery passed; no engine/server started")
}
