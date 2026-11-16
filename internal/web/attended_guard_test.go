package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
)

func TestAttendedWebCookieCSRFDoesNotBypassGuard(t *testing.T) {
	runner := dill.New("/must-not-execute")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/synthetic/socket"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
	ag := agent.NewLocalAgent(&dashboardEngine{}, runner)
	boundary, err := api.NewAttendedContext(ag, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "operator", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"user": {"operator"}, "password": {"password"}}
	login := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	a.HandleLogin(lw, login)
	if lw.Code != 303 || len(lw.Result().Cookies()) == 0 {
		t.Fatal("login failed")
	}
	cookie := lw.Result().Cookies()[0]
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Get("/csrf", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })
	if err := MountAttended(r, New(ag, nil, nil, nil, "docker", false, a, time.Second), httpx.Timeouts{}, boundary); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest("GET", "/csrf", nil)
	get.AddCookie(cookie)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, get)
	csrf := gw.Body.String()
	if csrf == "" {
		t.Fatal("no csrf")
	}
	for _, path := range []string{"/web/stacks/demo/staging/prepare", "/web/stacks/demo/staging/confirm", "/web/stacks/demo/validate", "/web/stacks/demo/fmt", "/web/stacks/demo/git/pull", "/web/containers/id/start", "/web/containers/id/stop", "/web/containers/id/restart", "/web/images/pull", "/web/stacks", "/web/stacks/git"} {
		for _, tc := range []struct {
			token  string
			status int
		}{{"", 403}, {"invalid", 403}, {csrf, 503}} {
			req := httptest.NewRequest("POST", path, nil)
			req.AddCookie(cookie)
			if tc.token != "" {
				req.Header.Set("X-CSRF-Token", tc.token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("csrf guard %s: got %d want %d", path, w.Code, tc.status)
			}
		}
	}
}

func TestAttendedWebGuardDeniesLegacyEvaluationAndMutation(t *testing.T) {
	runner := dill.New("/must-not-execute")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/synthetic/socket"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
	ag := agent.NewLocalAgent(&dashboardEngine{}, runner)
	boundary, err := api.NewAttendedContext(ag, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(ag, nil, nil, nil, "docker", false, nil, time.Second)
	r := chi.NewRouter()
	if err := MountAttended(r, h, httpx.Timeouts{}, boundary); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/web/stacks/demo/plan"}, {"GET", "/web/stacks/demo/containers"}, {"POST", "/web/stacks/demo/validate"}, {"POST", "/web/stacks/demo/fmt"}, {"POST", "/web/stacks/demo/git/pull"}, {"POST", "/web/containers/id/start"}, {"POST", "/web/images/pull"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != 503 {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
