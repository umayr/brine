package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/store"
)

func TestStackPathResolverFindsSafeSibling(t *testing.T) {
	st, e := store.NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	stack, e := st.Create("demo", "recipe")
	if e != nil {
		t.Fatal(e)
	}
	if e = st.WriteFile("demo", "service.pkl", "sibling"); e != nil {
		t.Fatal(e)
	}
	resolve := stackPathResolver(st)
	name, e := resolve(filepath.Join(filepath.Dir(stack.PklPath), "service.pkl"))
	if e != nil || name != "demo" {
		t.Fatalf("sibling resolution: %q %v", name, e)
	}
	if _, e = resolve(filepath.Join(filepath.Dir(stack.PklPath), "missing.pkl")); e == nil {
		t.Fatal("missing file resolved")
	}
	outside := filepath.Join(t.TempDir(), "outside.pkl")
	if e = os.WriteFile(outside, []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(filepath.Dir(stack.PklPath), "link.pkl")); e != nil {
		t.Fatal(e)
	}
	if _, e = resolve(filepath.Join(filepath.Dir(stack.PklPath), "link.pkl")); e == nil {
		t.Fatal("symlink resolved")
	}
}
func TestRecipeCannotRedirectAliasToMarkedStackWithOldDill(t *testing.T) {
	st, e := store.NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	alias, e := st.Create("alias", `config { name = "demo" }`)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "old-dill")
	mutation := filepath.Join(dir, "mutation")
	script := "#!/bin/sh\ncase \" $* \" in *\" --engine-socket \"*) exit 2;; esac\ntouch '" + mutation + "'\n"
	if e = os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	eng := gateEngine{containers: []engine.ContainerInfo{{ID: "demo-web", Stack: "demo", Labels: map[string]string{"dill.managed": "true", "dill.stack": "demo", "dill.service": "web", "dill.secret-enabled": "true"}}}}
	r := dill.New(bin)
	r.Engine = "docker"
	r.Socket = "/synthetic/explicit.sock"
	r.ResolveStack = stackPathResolver(st)
	r.BeforeLegacyAction = legacySecretGate(st, eng, nil)
	var out bytes.Buffer
	ctx := context.Background()
	for _, f := range []func() error{func() error { return r.Up(ctx, alias.PklPath, &out) }, func() error { return r.Start(ctx, alias.PklPath, &out) }, func() error { return r.Restart(ctx, alias.PklPath, &out) }} {
		if e = f(); e == nil || !strings.Contains(e.Error(), "endpoint-pinning") {
			t.Fatalf("old binary should fail closed: %v", e)
		}
	}
	if _, e = os.Stat(mutation); !os.IsNotExist(e) {
		t.Fatal("old binary mutation reached")
	}
}
func TestRelativeDataDirIsNormalizedBeforeStackPaths(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = "relative-brine-data"
	expected, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = normalizeDataDir(cfg); err != nil || cfg.DataDir != expected {
		t.Fatalf("data directory normalization: %q %v", cfg.DataDir, err)
	}
}

func TestEffectiveLegacyHostUsesResolvedInventoryEndpoint(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///synthetic/nondefault.sock")
	cfg := config.Default()
	cfg.EngineSocket = ""
	host := effectiveEngineHost(cfg)
	if host != "unix:///synthetic/nondefault.sock" {
		t.Fatalf("unexpected resolved host: %s", host)
	}
	cfg.EngineSocket = "/synthetic/operator.sock"
	if got := effectiveEngineHost(cfg); got != "unix:///synthetic/operator.sock" {
		t.Fatalf("unexpected explicit host: %s", got)
	}
}
