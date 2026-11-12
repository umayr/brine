package dill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandDoesNotInheritServerCredentials(t *testing.T) {
	for _, key := range []string{"BRINE_AUTH_USER", "BRINE_AUTH_PASSWORD", "BRINE_API_TOKEN"} {
		t.Setenv(key, "synthetic-auth-canary")
	}
	t.Setenv("BRINE_SYNTHETIC_DEPLOYMENT", "synthetic-deployment-canary")
	cmd, cleanup, err := New("dill").command(context.Background(), "validate")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	env := strings.Join(cmd.Env, "\n")
	if strings.Contains(env, "synthetic-auth-canary") {
		t.Fatal("server credentials inherited")
	}
	if strings.Contains(env, "synthetic-deployment-canary") {
		t.Fatal("arbitrary server environment inherited")
	}
}

func TestBareBinaryDoesNotResolveFromAmbientPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-dill")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	cmd, cleanup, err := New("synthetic-dill").command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cmd.Path == path {
		t.Fatal("binary resolved through ambient PATH")
	}
}
