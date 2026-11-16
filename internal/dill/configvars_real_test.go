package dill

import (
	"context"
	"fmt"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/store"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in local test: use an explicitly selected Dill, Pkl, and matching schema.
// It performs validation only; it does not contact an engine or deploy.
func TestConfigVariablesRealDill(t *testing.T) {
	binary, pkl, schema := os.Getenv("BRINE_TEST_DILL"), os.Getenv("BRINE_TEST_PKL"), os.Getenv("BRINE_TEST_DILL_SCHEMA")
	if binary == "" || pkl == "" || schema == "" {
		t.Skip("set BRINE_TEST_DILL, BRINE_TEST_PKL and BRINE_TEST_DILL_SCHEMA")
	}
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("amends %q\nlocal actual: \"local\" = read(\"env:BRINE_VAR_DOMAIN\")\nconfig { name = actual; services { [\"web\"] { image = \"nginx:alpine\" } } }\n", "file://"+filepath.ToSlash(schema))
	stack, err := st.Create("app", content)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := configvars.New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if err = vars.Set("", "DOMAIN", "global", false); err != nil {
		t.Fatal(err)
	}
	if err = vars.Set("app", "DOMAIN", "local", false); err != nil {
		t.Fatal(err)
	}
	runner := New(binary)
	runner.Variables = vars
	runner.PklBinary = pkl
	runner.Engine = "docker"
	runner.Socket = "/var/run/docker.sock"
	runner.ResolveStack = func(string) (string, error) { return "app", nil }
	if err = runner.Validate(context.Background(), stack.PklPath); err != nil {
		t.Fatalf("real validation: %v", err)
	}
	if err = vars.Set("app", "DOMAIN", "wrong", false); err != nil {
		t.Fatal(err)
	}
	if err = runner.Validate(context.Background(), stack.PklPath); err == nil {
		t.Fatal("invalid override unexpectedly accepted")
	}
}
