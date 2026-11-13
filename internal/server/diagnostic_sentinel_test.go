package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/engine"
	"github.com/umayr/brine/internal/store"
)

type sentinelLogEngine struct {
	engine.Engine // Unexpected engine calls fail rather than contact a daemon.
	calls, closes int
}

type sentinelLogReader struct {
	io.Reader
	closed func()
}

func (r *sentinelLogReader) Close() error { r.closed(); return nil }

func (e *sentinelLogEngine) ContainerLogs(_ context.Context, id string, _ bool, _ int) (io.ReadCloser, error) {
	e.calls++
	if id == "failure" {
		return nil, errors.New("synthetic-deployment-engine-error")
	}
	return &sentinelLogReader{Reader: strings.NewReader("synthetic-deployment-engine-stdout\nsynthetic-deployment-engine-stderr\n"), closed: func() { e.closes++ }}, nil
}

// Producer-to-sink sentinel matrix (all synthetic; paired package tests):
//
// Producer / value class         Sink                          Expected
// parent login/API environment  Dill -> HTTP/streams/state    forbidden
// uploaded binding plaintext    API errors/logs/private state forbidden (api)
// strict raw stdout/stderr      errors/results/lifecycle      suppressed (api/dill)
// valid strict plan field       API result/private preview    operator-visible (api/dill)
// legacy Dill stdout/stderr     errors/lifecycle/cleanup      operator-visible
// engine log/error producer     BOTH API and web/SSE routes   operator-visible
// Git stderr (inherited env)    service logs + meta.json      operator-visible (git)
// any unauthorized request      producer invocation           forbidden
//
// Login/API credentials are supplied to auth + the parent environment; the fake
// Dill subprocess actively tries to echo those env values. Deployment markers
// are actually emitted by producers, not merely searched for at unrelated sinks.
// This is not universal redaction: operators must protect deployment logs/state.
func TestDiagnosticSentinelAuthenticatedProducerToHTTP(t *testing.T) {
	const password = "synthetic-forbidden-login-credential"
	const token = "synthetic-forbidden-api-credential"
	t.Setenv("BRINE_AUTH_PASSWORD", password)
	t.Setenv("BRINE_API_TOKEN", token)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	homes := filepath.Join(dir, "homes")
	binary := filepath.Join(dir, "dill")
	script := fmt.Sprintf(`#!/bin/sh
printf 'called\n' >> %q
printf '%%s\n' "$HOME" >> %q
printf '%%s\n' "${BRINE_AUTH_PASSWORD}" "${BRINE_API_TOKEN}"
printf 'synthetic-deployment-dill-stdout\n'
printf 'synthetic-deployment-dill-stderr\n' >&2
exit 2
`, calls, homes)
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewFSStore(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("demo", "x"); err != nil {
		t.Fatal(err)
	}
	runner := dill.New(binary)
	runner.Engine, runner.Socket = "docker", "/tmp/synthetic-never-contacted.sock"
	runner.ResolveStack = func(string) (string, error) { return "demo", nil }
	eng := &sentinelLogEngine{}
	a, err := auth.New(auth.Config{User: "admin", Password: password, APIToken: token})
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), agent.NewLocalAgent(eng, runner), st, nil, nil, a)
	var serviceLogs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&serviceLogs)
	defer log.SetOutput(old)
	for _, row := range []struct {
		method, path, producer string
		stream                 bool
	}{
		{"GET", "/api/containers/demo/logs", "engine", true},
		{"GET", "/web/containers/demo/logs", "engine", true},
		{"GET", "/api/containers/failure/logs", "engine-error", false},
		{"GET", "/web/containers/failure/logs", "engine-error", false},
		{"GET", "/api/stacks/demo/validate", "dill", false},
		{"POST", "/web/stacks/demo/validate", "dill", false},
		{"GET", "/api/stacks/demo/plan", "dill", false},
		{"POST", "/api/stacks/demo/up", "dill", true},
		{"POST", "/api/stacks/demo/start", "dill", true},
		{"POST", "/api/stacks/demo/restart", "dill", true},
		{"POST", "/api/stacks/demo/stop", "dill", true},
		{"POST", "/api/stacks/demo/down", "dill", true},
		{"POST", "/api/stacks/demo/teardown", "dill", true},
	} {
		t.Run(row.path, func(t *testing.T) {
			beforeEngine := eng.calls
			beforeCalls, _ := os.ReadFile(calls)
			request := func(authorized bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(row.method, row.path, nil)
				r.Header.Set("Accept", "application/json")
				if authorized {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				s.router.ServeHTTP(w, r)
				for _, forbidden := range []string{password, token} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Fatal("credential reached HTTP sink")
					}
				}
				return w
			}
			w := request(false)
			if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "synthetic-deployment") {
				t.Fatal("unauthorized diagnostic exposure")
			}
			afterCalls, _ := os.ReadFile(calls)
			if eng.calls != beforeEngine || string(beforeCalls) != string(afterCalls) {
				t.Fatal("unauthorized request reached producer")
			}
			w = request(true)
			if row.producer == "dill" {
				if !strings.Contains(w.Body.String(), "synthetic-deployment-dill-stdout") || !strings.Contains(w.Body.String(), "synthetic-deployment-dill-stderr") {
					t.Fatalf("authenticated Dill diagnostics not exercised: status=%d", w.Code)
				}
			} else if row.producer == "engine-error" {
				if w.Code != 500 || !strings.Contains(w.Body.String(), "synthetic-deployment-engine-error") {
					t.Fatal("engine error diagnostic not exercised")
				}
			} else {
				if !strings.Contains(w.Body.String(), "synthetic-deployment-engine-stdout") || !strings.Contains(w.Body.String(), "synthetic-deployment-engine-stderr") || !strings.Contains(w.Body.String(), "event: done") {
					t.Fatal("authenticated SSE diagnostics not exercised")
				}
			}
		})
	}
	if eng.closes != 2 {
		t.Fatal("log readers were not closed")
	}
	data, err := os.ReadFile(homes)
	if err != nil {
		t.Fatal("subprocess producer never executed")
	}
	for _, home := range strings.Fields(string(data)) {
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("subprocess runtime HOME retained")
		}
	}
	for _, forbidden := range []string{password, token} {
		if strings.Contains(serviceLogs.String(), forbidden) {
			t.Fatal("credential reached service logs")
		}
	}
	// No diagnostics are silently persisted by these HTTP-only operations.
	err = filepath.WalkDir(filepath.Join(dir, "state"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for _, marker := range []string{password, token, "synthetic-deployment"} {
			if bytes.Contains(b, []byte(marker)) {
				t.Fatal("HTTP diagnostic persisted")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
