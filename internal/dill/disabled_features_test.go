package dill

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/umayr/brine/internal/secretbindings"
)

func TestDisabledDirectSecretAndAlphaOperations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, ".brine-disabled-secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	workspace, private := filepath.Join(dir, "workspace"), filepath.Join(dir, "private")
	for _, path := range []string{workspace, private} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	id := secretbindings.DeploymentID("demo")
	if err := state.SaveDraft(id, "demo", "lab-a", []byte(testBinding)); err != nil {
		t.Fatal(err)
	}
	if err := state.MarkSecretEnabled(id); err != nil {
		t.Fatal(err)
	}
	proof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	if err := state.SavePreview(id, "demo", "lab-a", []byte(testBinding), proof, token, []byte(`{"changes":[]}`), 1); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		m := map[string]string{}
		if err := filepath.WalkDir(private, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			m[path] = string(b)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := snapshot()
	marker, binary := filepath.Join(dir, "called"), filepath.Join(dir, "dill")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := New(binary)
	s := &SecretRunner{Runner: runner, State: state, Coordinator: &runner.Coordinator, Engine: "docker", Socket: "/synthetic/socket", Policy: "/synthetic/policy", Target: "lab-a"}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"preview":        func() error { _, e := s.Preview(ctx, id, "demo", "/synthetic/config"); return e },
		"preview locked": func() error { _, _, e := s.PreviewLocked(ctx, id, "demo", "/synthetic/config"); return e },
		"apply active":   func() error { return s.ApplyActive(ctx, id, "demo", "/synthetic/config") },
		"confirm":        func() error { return s.ConfirmAndApply(ctx, id, "demo", "/synthetic/config", token) },
		"confirm locked": func() error { return s.ConfirmAndApplyLocked(ctx, id, "demo", "/synthetic/config", token) },
		"check active":   func() error { return s.CheckActive(ctx, id, "demo", "/synthetic/config") },
		"check draft":    func() error { return s.CheckDraft(ctx, id, "demo", "/synthetic/config") },
		"check locked":   func() error { return s.CheckLocked(ctx, id, "demo", "/synthetic/config", true) },
		"discovery":      func() error { return runner.CheckSecretCapabilities(ctx, "docker") },
	} {
		if err := call(); !errors.Is(err, ErrSecretUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("disabled secret path launched child: %v", err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("disabled secret path changed persisted state")
	}
	r := New("not-executed")
	if _, err := r.PreviewPreflight(context.Background(), "", "up", false, PreflightBinding{}, nil); !errors.Is(err, ErrAlphaDisabled) {
		t.Fatalf("alpha: %v", err)
	}
	r.StrictActions = preflightStrictStub{}
	for name, call := range map[string]func() error{
		"up":               func() error { return r.Up(ctx, "/synthetic", nil) },
		"start":            func() error { return r.Start(ctx, "/synthetic", nil) },
		"restart":          func() error { return r.Restart(ctx, "/synthetic", nil) },
		"plan":             func() error { _, e := r.Plan(ctx, "/synthetic"); return e },
		"validate":         func() error { return r.Validate(ctx, "/synthetic") },
		"validate content": func() error { return r.ValidateContent(ctx, "/synthetic", "x") },
	} {
		if err := call(); !errors.Is(err, ErrSecretUnavailable) {
			t.Fatalf("injected strict %s: %v", name, err)
		}
	}
	r.Profile = "alpha"
	if err := r.Stop(context.Background(), "/synthetic", nil); !errors.Is(err, ErrAlphaDisabled) {
		t.Fatalf("alpha stop: %v", err)
	}
	if _, err := r.PS(context.Background(), "/synthetic"); !errors.Is(err, ErrAlphaDisabled) {
		t.Fatalf("alpha ps: %v", err)
	}
}

func TestDisabledOperationDoesNotStartObserverOrWrite(t *testing.T) {
	started, terminal := 0, 0
	observer := testObserver{start: func() error { started++; return nil }, finish: func(OperationInvocation) error { terminal++; return nil }}
	ctx := WithOperationObserver(context.Background(), observer)
	r := New("/must-not-execute")
	r.QualifiedOperation = &QualifiedOperationConfig{}
	var out bytes.Buffer
	_, err := r.operation(ctx, "/synthetic", "up", &out)
	if !errors.Is(err, ErrAlphaDisabled) || started != 0 || terminal != 0 || out.Len() != 0 {
		t.Fatalf("disabled operation: err=%v started=%d terminal=%d output=%q", err, started, terminal, out.String())
	}
}
