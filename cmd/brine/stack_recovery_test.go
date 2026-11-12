package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/store"
)

func TestUnsafeOrMissingEntrypointRemainsDiscoverableAndRecoverable(t *testing.T) {
	for _, kind := range []string{"missing", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			st, err := store.NewFSStore(filepath.Join(root, "data"))
			if err != nil {
				t.Fatal(err)
			}
			repo, err := st.RepoDir("demo")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(repo, 0700); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(repo, "compose.pkl")
			if err = os.WriteFile(entry, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			stack, err := st.CreateFromGit("demo", &store.GitSource{URL: "synthetic"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(entry); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			if err = os.WriteFile(outside, []byte("sensitive-synthetic"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err = os.Symlink(outside, entry); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = st.Get("demo"); err != nil {
				t.Fatalf("metadata disappeared: %v", err)
			}
			list, err := st.List()
			if err != nil || len(list) != 1 || list[0].Name != "demo" {
				t.Fatal("stack missing from inventory")
			}
			if _, err = st.ReadPkl("demo"); err == nil {
				t.Fatal("unsafe content read")
			}
			if _, err = st.ReadFile("demo", "compose.pkl"); err == nil {
				t.Fatal("unsafe file read")
			}
			log := filepath.Join(root, "calls")
			bin := filepath.Join(root, "dill")
			script := "#!/bin/sh\n[ \"$1\" = -f ] || exit 8\ncase \" $* \" in *\" -p demo --engine docker --engine-socket /synthetic/engine.sock \"*) ;; *) exit 9;; esac\ncase \" $* \" in *\" stop \"*|*\" down \"*) [ \"$2\" = \"$HOME/unavailable-config.pkl\" ] && [ ! -e \"$2\" ] || exit 10;; esac\nprintf '%s\\n' \"$*\" >> '" + log + "'\nexit 0\n"
			if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			runner := dill.New(bin)
			runner.Engine = "docker"
			runner.Socket = "/synthetic/engine.sock"
			runner.ResolveStack = stackPathResolver(st)
			runner.CheckSource = stackSourceCheck(st)
			ctx := context.Background()
			var out bytes.Buffer
			for _, action := range []func() error{func() error { return runner.Up(ctx, stack.PklPath, &out) }, func() error { return runner.Start(ctx, stack.PklPath, &out) }, func() error { return runner.Restart(ctx, stack.PklPath, &out) }, func() error { _, e := runner.Plan(ctx, stack.PklPath); return e }, func() error { return runner.Validate(ctx, stack.PklPath) }, func() error { return runner.Teardown(ctx, stack.PklPath, false, &out) }} {
				if err = action(); err == nil {
					t.Fatal("unsafe source-consuming action accepted")
				}
			}
			if _, err = os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("unsafe action ran Dill")
			}
			if err = runner.Stop(ctx, stack.PklPath, &out); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if err = runner.Down(ctx, stack.PklPath, &out); err != nil {
				t.Fatalf("down: %v", err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || strings.Count(string(calls), "unavailable-config.pkl") != 2 || strings.Contains(string(calls), entry) {
				t.Fatal("cleanup did not use private nonexistent config")
			}
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					t.Fatal("bad cleanup invocation")
				}
				if _, err = os.Stat(filepath.Dir(fields[1])); !os.IsNotExist(err) {
					t.Fatal("cleanup runtime directory not removed")
				}
			}
			if err = st.WriteFile("demo", "repair.pkl", "repair"); err != nil {
				t.Fatalf("editor repair: %v", err)
			}
			if err = st.UpdateGitSource("demo", &store.GitSource{URL: "synthetic-repair"}); err != nil {
				t.Fatalf("git source repair: %v", err)
			}
			if err = st.SetEntrypoint("demo", "repair.pkl"); err != nil {
				t.Fatalf("entrypoint repair: %v", err)
			}
			repaired, err := st.ReadPkl("demo")
			if err != nil || repaired != "repair" {
				t.Fatal("repaired file unavailable")
			}
			outsideBytes, err := os.ReadFile(outside)
			if err != nil || string(outsideBytes) != "sensitive-synthetic" {
				t.Fatal("unsafe target changed")
			}
		})
	}
}

func TestUnsafeOrMissingWorkspaceCanBeReconfigured(t *testing.T) {
	for _, kind := range []string{"missing", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			st, err := store.NewFSStore(filepath.Join(root, "data"))
			if err != nil {
				t.Fatal(err)
			}
			repo, err := st.RepoDir("demo")
			if err != nil {
				t.Fatal(err)
			}
			folder := filepath.Join(repo, "nested")
			if err = os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(folder, "compose.pkl"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("repaired"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = st.CreateFromGit("demo", &store.GitSource{URL: "synthetic", Folder: "nested"}, ""); err != nil {
				t.Fatal(err)
			}
			if err = os.RemoveAll(folder); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			if err = os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(outside, "compose.pkl"), []byte("external"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err = os.Symlink(outside, folder); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = st.Get("demo"); err != nil {
				t.Fatalf("metadata unavailable: %v", err)
			}
			if _, err = st.ReadPkl("demo"); err == nil {
				t.Fatal("unsafe workspace content accepted")
			}
			if err = st.UpdateGitSource("demo", &store.GitSource{URL: "synthetic", Folder: ""}); err != nil {
				t.Fatalf("folder recovery failed: %v", err)
			}
			if content, err := st.ReadPkl("demo"); err != nil || content != "repaired" {
				t.Fatalf("safe folder unavailable: %v", err)
			}
		})
	}
}
