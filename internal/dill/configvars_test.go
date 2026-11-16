package dill

import (
	"context"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/store"
)

func TestScopedConfigurationEnvironment(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("app", "config {}")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := configvars.New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if err = vars.Set("", "DOMAIN", "shared", false); err != nil {
		t.Fatal(err)
	}
	if err = vars.Set("app", "DOMAIN", "local", false); err != nil {
		t.Fatal(err)
	}
	r := New("dill")
	r.Variables = vars
	r.ResolveStack = func(path string) (string, error) {
		if path != stack.PklPath {
			t.Fatalf("unexpected path %s", path)
		}
		return "app", nil
	}
	cmd, cleanup, err := r.command(context.Background(), "-f", stack.PklPath, "validate")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := strings.Join(cmd.Env, "\n")
	if !strings.Contains(joined, "BRINE_VAR_DOMAIN=local") {
		t.Fatal("missing override")
	}
	r.Profile = "alpha"
	cmd, cleanup2, err := r.command(context.Background(), "-f", stack.PklPath, "validate")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if strings.Contains(strings.Join(cmd.Env, "\n"), "BRINE_VAR_DOMAIN=") {
		t.Fatal("leaked into alpha")
	}
}
