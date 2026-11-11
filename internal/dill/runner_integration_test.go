package dill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReleasedDill050 exercises Runner against the released CLI, not a stub.
func TestReleasedDill050(t *testing.T) {
	binary := os.Getenv("BRINE_TEST_DILL")
	if binary == "" {
		t.Skip("set BRINE_TEST_DILL and BRINE_TEST_DILL_SCHEMA to opt in")
	}
	schema := os.Getenv("BRINE_TEST_DILL_SCHEMA")
	if schema == "" {
		t.Fatal("BRINE_TEST_DILL_SCHEMA must point to dill.pkl from the v0.5.0 release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "v0.5.0" {
		t.Fatalf("expected released Dill v0.5.0: output %q, error %v", out, err)
	}
	data, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dill.pkl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.pkl")
	const valid = `amends "dill.pkl"
config {
  name = "brine-integration"
  engine = "docker"
  services {
    ["web"] { image = "nginx:alpine" }
  }
}
`
	const invalid = "broken =\n"
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	runner := New(binary)
	t.Run("Validate", func(t *testing.T) {
		if err := runner.Validate(ctx, path); err != nil {
			t.Fatalf("valid file: %v", err)
		}
		badPath := filepath.Join(dir, "invalid.pkl")
		if err := os.WriteFile(badPath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runner.Validate(ctx, badPath); err == nil {
			t.Fatal("invalid file accepted")
		}
	})
	t.Run("ValidateContent", func(t *testing.T) {
		if err := runner.ValidateContent(ctx, path, valid); err != nil {
			t.Fatalf("valid stdin with relative schema: %v", err)
		}
		if err := runner.ValidateContent(ctx, path, invalid); err == nil {
			t.Fatal("invalid stdin accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != valid {
			t.Fatalf("stdin validation modified file: %q, %v", after, err)
		}
	})
	t.Run("Fmt", func(t *testing.T) {
		formatted, err := runner.Fmt(ctx, path, "x=1\n")
		if err != nil || formatted != "x = 1\n" {
			t.Fatalf("format: %q, %v", formatted, err)
		}
		again, err := runner.Fmt(ctx, path, formatted)
		if err != nil || again != formatted {
			t.Fatalf("format not idempotent: %q, %v", again, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != valid {
			t.Fatalf("stdin formatting modified file: %q, %v", after, err)
		}
	})
}
