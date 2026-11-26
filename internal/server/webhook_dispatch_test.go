package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

// This explicit executor fixture records runner invocation and returns a fixed
// plan. It never connects to a container engine or runs a deployment.
func dispatcherServer(t *testing.T) (*Server, *store.FSStore, *dill.Runner, string) {
	t.Helper()
	original := webhookServer(t)
	cfg := original.cfg
	st, e := store.NewFSStore(cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	stack, e := st.Create("example", "config { engine = \"docker\" }\n")
	if e != nil {
		t.Fatal(e)
	}
	stub := filepath.Join(t.TempDir(), "synthetic-dill")
	if e = os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$0.calls\"\ncase \" $* \" in *' plan --format json '*) printf '%s\\n' '{\"changes\":[]}' ;; esac\n"), 0700); e != nil {
		t.Fatal(e)
	}
	runner := dill.New(stub)
	runner.Engine = "docker"
	runner.Socket = filepath.Join(t.TempDir(), "not-an-engine.sock")
	runner.ResolveStack = func(path string) (string, error) {
		current, e := st.Get("example")
		if e != nil || current.PklPath != path {
			return "", webhooks.ErrUnsupported
		}
		return current.Name, nil
	}
	runner.CheckSource = func(path string) error { _, e := st.ReadFile("example", filepath.Base(path)); return e }
	runner.BeforeLegacyAction = func(context.Context, string) error { return nil }
	s := New(cfg, agent.NewLocalAgent(nil, runner), st, nil, git.New(), original.authn)
	svc, e := webhooks.OpenExisting(webhooks.Config{Directory: cfg.WebhooksDir, EncryptionKey: []byte{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4}})
	if e != nil {
		t.Fatal(e)
	}
	s.webhooks = svc
	s.webhookDispatcher = &webhookDispatcher{s: s}
	s.web.EnableWebhooks(svc, s.webhookDispatcher)
	t.Cleanup(func() { svc.Close() })
	_ = stack
	return s, st, runner, stub + ".calls"
}
func TestSignedRequestUsesRealAdmissionAndRunnerOnce(t *testing.T) {
	s, st, _, logPath := dispatcherServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.webhooks.RunTriggers(ctx, s.webhookDispatcher)
	d := s.webhookDispatcher.(*webhookDispatcher)
	stack, _ := st.Get("example")
	tr, key, e := s.webhooks.SaveTrigger(ctx, webhooks.Trigger{Name: "plan", StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "plan", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	body := `{"id":"release-one"}`
	send := func() *httptest.ResponseRecorder {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		r := httptest.NewRequest("POST", "/hooks/"+tr.ID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Brine-Timestamp", ts)
		r.Header.Set("Brine-Signature", webhooks.Sign([]byte(key), ts, []byte(body)))
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		return w
	}
	if w := send(); w.Code != 202 {
		t.Fatalf("acceptance: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		rows, e := s.webhooks.Requests(ctx, tr.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) == 1 && rows[0].Status == "completed" {
			if rows[0].Outcome != "success" {
				t.Fatal("plan outcome")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request not executed: %+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := send(); w.Code != 202 {
		t.Fatal("duplicate not acknowledged", w.Code)
	}
	bytes, e := os.ReadFile(logPath)
	if e != nil || strings.Count(string(bytes), " plan --format json") != 1 {
		t.Fatalf("runner invocations: %q %v", bytes, e)
	}
	if !strings.Contains(string(bytes), "--engine docker --engine-socket") {
		t.Fatal("explicit target pinning lost")
	}
}
func TestDispatcherDeniesChangedAndProtectedBindings(t *testing.T) {
	s, st, r, logPath := dispatcherServer(t)
	d := s.webhookDispatcher.(*webhookDispatcher)
	stack, _ := st.Get("example")
	grant := webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "restart"}
	if e := d.Validate(context.Background(), grant); e != nil {
		t.Fatal("fixture not admitted", e)
	}
	r.BeforeLegacyAction = func(context.Context, string) error { return dill.ErrSecretUnavailable }
	if d.Validate(context.Background(), grant) == nil {
		t.Fatal("protected state bypass")
	}
	r.BeforeLegacyAction = func(context.Context, string) error { return nil }
	r.CheckSourceProtection = func(string) error { return dill.ErrSecretUnavailable }
	if d.Validate(context.Background(), grant) == nil {
		t.Fatal("source protection bypass")
	}
	r.CheckSourceProtection = nil
	bad := grant
	bad.TargetID = "another-target"
	if d.Validate(context.Background(), bad) == nil {
		t.Fatal("target override")
	}
	if _, e := st.Update("example", "config { engine = \"podman\" }\n"); e != nil {
		t.Fatal(e)
	}
	if d.Validate(context.Background(), grant) == nil {
		t.Fatal("engine mismatch")
	}
	st.Delete("example")
	st.Create("example", "config { engine = \"docker\" }\n")
	if d.Validate(context.Background(), grant) == nil {
		t.Fatal("recreated stack inherited grant")
	}
	if _, e := os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("denial executed a child")
	}
}
func TestDispatcherSupportsLocalActionsAndKeepsReviewBoundary(t *testing.T) {
	for _, action := range []string{"deploy", "start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			s, st, _, logPath := dispatcherServer(t)
			d := s.webhookDispatcher.(*webhookDispatcher)
			stack, _ := st.Get("example")
			grant := webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: action}
			result := d.Execute(context.Background(), grant, webhooks.IncomingRequest{ID: "req_fixture_" + action})
			if result.Status != "completed" {
				t.Fatalf("%s: %+v", action, result)
			}
			if _, e := os.Stat(logPath); e != nil {
				t.Fatal("runner not invoked")
			}
		})
	}
	s, st, _, logPath := dispatcherServer(t)
	s.cfg.StagingSocket = "/synthetic/review-required.sock"
	d := s.webhookDispatcher.(*webhookDispatcher)
	stack, _ := st.Get("example")
	result := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "deploy"}, webhooks.IncomingRequest{ID: "req_review"})
	if result.Status != "awaiting_review" {
		t.Fatal("review bypassed", result)
	}
	if _, e := os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("reviewed deployment ran automatically")
	}
}
