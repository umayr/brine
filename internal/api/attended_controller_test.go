package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
)

func TestAttendedFieldsClosedAndBounded(t *testing.T) {
	for _, raw := range []string{`{}`, `{"action":null}`, `{"action":2}`, `{"action":"up","action":"up"}`, `{"action":"up","extra":false}`, `{"action":"up"}{}`, `{"action":"up"}` + strings.Repeat(" ", 4097)} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		if _, err := attendedFields(req, "action"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"up"}`))
	got, err := attendedFields(req, "action")
	if err != nil || got["action"] != "up" {
		t.Fatal(got, err)
	}
}

func TestAttendedUnavailableDeniesAllFiveAndLegacy(t *testing.T) {
	runner := dill.New("/must-not-execute")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/synthetic/socket"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
	ag := agent.NewLocalAgent(&secretRouteEngine{}, runner)
	boundary, err := NewAttendedContext(ag, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	authn, err := auth.New(auth.Config{User: "operator", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(authn.Middleware)
	if err := MountAttended(r, ag, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second, boundary, nil); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"up", "start", "restart", "stop", "down"} {
		req := httptest.NewRequest("POST", "/api/stacks/demo/"+action, strings.NewReader(`{"requestId":"x","reviewDigest":"x","confirmationToken":"x"}`))
		req.Header.Set("Authorization", "Bearer synthetic-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/stacks/demo/operations/preview", "/api/stacks/demo/teardown", "/api/containers/id/start", "/api/images/pull"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"action":"up"}`))
		req.Header.Set("Authorization", "Bearer synthetic-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/stacks/demo/up", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}
