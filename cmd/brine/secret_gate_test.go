package main

import (
	"bytes"
	"context"
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
}
