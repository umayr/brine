package dill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resolveIntegrationPkl() (string, error) {
	name := os.Getenv("DILL_PKL_PATH")
	if name == "" {
		name = "pkl"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("Pkl prerequisite %q: %w", name, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("Pkl prerequisite %q: %w", name, err)
	}
	return path, nil
}

func TestResolveIntegrationPklMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DILL_PKL_PATH", "")
	if _, err := resolveIntegrationPkl(); err == nil {
		t.Fatal("missing Pkl accepted")
	}
	t.Setenv("DILL_PKL_PATH", filepath.Join(t.TempDir(), "missing-pkl"))
	if _, err := resolveIntegrationPkl(); err == nil {
		t.Fatal("missing explicit Pkl accepted")
	}
}

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
	pkl, err := resolveIntegrationPkl()
	if err != nil {
		t.Fatal(err)
	}
	// Prevent Dill from falling back to its user cache or downloader.
	t.Setenv("DILL_PKL_PATH", pkl)
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
	assertInvalidJSON := func(t *testing.T, cmd *exec.Cmd) {
		t.Helper()
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("invalid validation must exit zero: %v", err)
		}
		var result struct {
			Valid *bool  `json:"valid"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.Valid == nil || *result.Valid || !strings.Contains(result.Error, "Unexpected end of file") {
			t.Fatalf("unexpected invalid validation JSON: %q, %v", out, err)
		}
	}
	t.Run("Validate", func(t *testing.T) {
		if err := runner.Validate(ctx, path); err != nil {
			t.Fatalf("valid file: %v", err)
		}
		badPath := filepath.Join(dir, "invalid.pkl")
		if err := os.WriteFile(badPath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		assertInvalidJSON(t, exec.CommandContext(ctx, binary, "-f", badPath, "validate", "--format", "json"))
		if err := runner.Validate(ctx, badPath); err == nil || !strings.Contains(err.Error(), "Unexpected end of file") {
			t.Fatalf("invalid file diagnostic: %v", err)
		}
	})
	t.Run("ValidateContent", func(t *testing.T) {
		if err := runner.ValidateContent(ctx, path, valid); err != nil {
			t.Fatalf("valid stdin with relative schema: %v", err)
		}
		cmd := exec.CommandContext(ctx, binary, "-f", path, "validate", "--stdin", "--format", "json")
		cmd.Stdin = strings.NewReader(invalid)
		assertInvalidJSON(t, cmd)
		if err := runner.ValidateContent(ctx, path, invalid); err == nil || !strings.Contains(err.Error(), "Unexpected end of file") {
			t.Fatalf("invalid stdin diagnostic: %v", err)
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
