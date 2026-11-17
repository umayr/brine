package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/store"
)

type gateEngine struct {
	engine.Engine
	containers []engine.ContainerInfo
}

func (e gateEngine) ListContainers(context.Context) ([]engine.ContainerInfo, error) {
	return e.containers, nil
}
func (e gateEngine) InspectContainer(_ context.Context, id string) (*engine.ContainerInfo, error) {
	for _, c := range e.containers {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, os.ErrNotExist
}
func TestLegacySecretGateBlocksMutationButNotCleanup(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "x")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "dill")
	if err = os.WriteFile(bin, []byte("#!/bin/sh\necho called\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e := gateEngine{containers: []engine.ContainerInfo{{ID: "id", Stack: "demo", Labels: map[string]string{"dill.stack": "demo", "dill.service": "web", "dill.managed": "true", "dill.secret-enabled": "true"}}}}
	r := dill.New(bin)
	r.Engine = "docker"
	r.Socket = "/synthetic/engine.sock"
	r.ResolveStack = stackPathResolver(st)
	r.BeforeLegacyAction = legacySecretGate(st, e, nil)
	var out bytes.Buffer
	ctx := context.Background()
	if r.Up(ctx, stack.PklPath, &out) == nil || r.Start(ctx, stack.PklPath, &out) == nil || r.Restart(ctx, stack.PklPath, &out) == nil {
		t.Fatal("secret downgrade permitted")
	}
	if out.Len() != 0 {
		t.Fatal("blocked operation ran dill")
	}
	if err = r.Down(ctx, stack.PklPath, &out); err != nil {
		t.Fatalf("cleanup blocked: %v", err)
	}
}

func fakeGateRunner(t *testing.T, st store.StackStore) (*dill.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	marker, binary := filepath.Join(dir, "calls"), filepath.Join(dir, "dill")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + marker + "'\necho '{\"valid\":true}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := dill.New(binary)
	r.Engine, r.Socket, r.ResolveStack = "docker", "/synthetic/engine.sock", stackPathResolver(st)
	return r, marker
}

func TestSecretRecordDeniesDowngradeAfterConfigurationRemoval(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-record-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	st, err := store.NewFSStore(data)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "x")
	if err != nil {
		t.Fatal(err)
	}
	state, err := openSecretState(data, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.MarkSecretEnabled(secretbindings.DeploymentID("demo")); err != nil {
		t.Fatal(err)
	}
	state.Close()
	restored, err := openSecretState(data, false)
	if err != nil || restored == nil {
		t.Fatalf("state unavailable: %v", err)
	}
	defer restored.Close()
	gate := legacySecretGate(st, gateEngine{}, restored)
	if err = gate(context.Background(), stack.PklPath); err == nil {
		t.Fatal("secret record permitted legacy downgrade")
	}
	r, marker := fakeGateRunner(t, st)
	r.BeforeLegacyAction = gate
	r.BeforeLegacyValidate = legacySecretRecordGate(st, restored)
	var out bytes.Buffer
	if err := r.Up(context.Background(), stack.PklPath, &out); !errors.Is(err, dill.ErrSecretUnavailable) {
		t.Fatalf("up: %v", err)
	}
	if err := r.Validate(context.Background(), stack.PklPath); !errors.Is(err, dill.ErrSecretUnavailable) {
		t.Fatalf("validate: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("protected stack invoked Dill: %v", err)
	}
	ordinary, err := st.Create("ordinary", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Up(context.Background(), ordinary.PklPath, &out); err != nil {
		t.Fatalf("ordinary up: %v", err)
	}
	if err := r.Validate(context.Background(), ordinary.PklPath); err != nil {
		t.Fatalf("ordinary validate: %v", err)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || bytes.Count(calls, []byte("\n")) != 2 {
		t.Fatalf("ordinary calls: %q %v", calls, err)
	}
	if enabled, err := restored.SecretEnabled(secretbindings.DeploymentID("demo")); err != nil || !enabled {
		t.Fatalf("secret classification changed: enabled=%v err=%v", enabled, err)
	}
}

func TestUnknownContainerHistoryDeniesLegacyExecution(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "x")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []engine.ContainerInfo{
		{ID: "missing-labels", Stack: "demo"},
		{ID: "unknown-secret-label", Stack: "demo", Labels: map[string]string{"dill.stack": "demo", "dill.service": "web", "dill.managed": "true", "dill.secret-enabled": "false"}},
		{Stack: "demo"},
	} {
		r, marker := fakeGateRunner(t, st)
		r.BeforeLegacyAction = legacySecretGate(st, gateEngine{containers: []engine.ContainerInfo{c}}, nil)
		var out bytes.Buffer
		for name, action := range map[string]func() error{
			"up":      func() error { return r.Up(context.Background(), stack.PklPath, &out) },
			"start":   func() error { return r.Start(context.Background(), stack.PklPath, &out) },
			"restart": func() error { return r.Restart(context.Background(), stack.PklPath, &out) },
		} {
			if err := action(); !errors.Is(err, dill.ErrSecretUnavailable) {
				t.Fatalf("%s unknown history %+v: %v", name, c, err)
			}
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unknown history invoked Dill: %v", err)
		}
		name := "ordinary-" + c.ID
		if c.ID == "" {
			name = "ordinary-empty"
		}
		ordinary, err := st.Create(name, "x")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Up(context.Background(), ordinary.PklPath, &out); err != nil {
			t.Fatalf("ordinary up: %v", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("ordinary up did not invoke Dill: %v", err)
		}
	}
}
