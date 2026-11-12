package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
)

func TestCookieMutationsRequireCSRF(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "synthetic-password"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=synthetic-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	s.router.ServeHTTP(response, login)
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login failed")
	}
	for _, path := range []string{"/api/stacks", "/web/stacks/example/up", "/api/images/pull"} {
		r := httptest.NewRequest("POST", path, nil)
		r.AddCookie(cookies[0])
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", path, w.Code)
		}
	}
}

func TestProtectedRoutesRejectUnauthenticated(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "synthetic-password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Default(), agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/stacks"}, {"DELETE", "/api/stacks/example"}, {"POST", "/web/stacks/example/up"},
		{"GET", "/api/containers/example/logs"}, {"GET", "/web/containers/example/logs"}, {"POST", "/api/images/pull"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if strings.Contains(w.Body.String(), "synthetic-") {
			t.Error("credential leaked")
		}
	}
}
