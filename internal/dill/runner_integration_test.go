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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "Pkl ") {
		return "", fmt.Errorf("Pkl prerequisite %q failed --version: %q, %v", path, out, err)
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

func TestResolveIntegrationPklRejectsNonPkl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-pkl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho not-pkl\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DILL_PKL_PATH", path)
	if _, err := resolveIntegrationPkl(); err == nil {
		t.Fatal("non-Pkl executable accepted")
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
	runner.PklBinary = pkl
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
	t.Run("PrivateHome", func(t *testing.T) {
		operatorHome := t.TempDir()
		t.Setenv("HOME", operatorHome)
		if err := os.WriteFile(filepath.Join(operatorHome, "credential-canary"), []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "runtime-home")
		wrapper := filepath.Join(dir, "real-dill-wrapper")
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		script := "#!/bin/sh\nprintf '%s' \"$HOME\" > " + quote(log) + "\nexec " + quote(binary) + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		r := New(wrapper)
		r.PklBinary = pkl
		if err := r.Validate(ctx, path); err != nil {
			t.Fatalf("released validation with isolated HOME: %v", err)
		}
		home, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if len(home) == 0 || string(home) == operatorHome {
			t.Fatal("operator HOME inherited")
		}
		if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
			t.Fatal("runtime HOME not removed")
		}
	})
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
	t.Run("RelativeImport", func(t *testing.T) {
		settings := filepath.Join(dir, "settings.pkl")
		if err := os.WriteFile(settings, []byte("image = \"nginx:alpine\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		const imported = `amends "dill.pkl"
import "settings.pkl"
config {
  name = "brine-integration"
  engine = "docker"
  services { ["web"] { image = settings.image } }
}
`
		importPath := filepath.Join(dir, "imported.pkl")
		if err := os.WriteFile(importPath, []byte(imported), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runner.Validate(ctx, importPath); err != nil {
			t.Fatalf("file relative import: %v", err)
		}
		if err := runner.ValidateContent(ctx, importPath, imported); err != nil {
			t.Fatalf("stdin relative import: %v", err)
		}
		if err := os.Remove(settings); err != nil {
			t.Fatal(err)
		}
		for name, validate := range map[string]func() error{
			"file":  func() error { return runner.Validate(ctx, importPath) },
			"stdin": func() error { return runner.ValidateContent(ctx, importPath, imported) },
		} {
			if err := validate(); err == nil || !strings.Contains(err.Error(), "settings.pkl") {
				t.Errorf("%s missing import diagnostic: %v", name, err)
			}
		}
	})
	t.Run("SchemaTypeError", func(t *testing.T) {
		bad := strings.Replace(valid, `image = "nginx:alpine"`, `image = 42`, 1)
		if err := runner.ValidateContent(ctx, path, bad); err == nil || !strings.Contains(err.Error(), "String") {
			t.Fatalf("schema type diagnostic: %v", err)
		}
	})
	t.Run("Fmt", func(t *testing.T) {
		formatted, err := runner.Fmt(ctx, path, "x=1\n")
		if err != nil || formatted != "x = 1\n" {
			t.Fatalf("format: %q, %v", formatted, err)
		}
		if _, err := runner.Fmt(ctx, path, invalid); err == nil {
			t.Fatal("invalid Pkl formatting succeeded")
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
