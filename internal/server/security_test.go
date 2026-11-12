package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
)

func TestAllRegisteredRoutesAreGuarded(t *testing.T) {
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
	params := regexp.MustCompile(`\{[^}]+\}`)
	count := 0
	err = chi.Walk(s.router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/login" || route == "/api/health" || strings.HasPrefix(route, "/static/") {
			return nil
		}
		path := params.ReplaceAllString(route, "example")
		path = strings.ReplaceAll(path, "*", "example")
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s: %d", method, route, w.Code)
		}
		if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
			r = httptest.NewRequest(method, path, nil)
			r.AddCookie(cookies[0])
			w = httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("missing CSRF %s %s: %d", method, route, w.Code)
			}
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 50 {
		t.Fatalf("unexpected route count: %d", count)
	}
}

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
