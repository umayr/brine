package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

func TestVariablesRoutes(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("app", "config {}"); err != nil {
		t.Fatal(err)
	}
	v, err := configvars.New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	runner := dill.New("dill")
	runner.Variables = v
	ag := agent.NewLocalAgent(nil, runner)
	h := New(ag, st, nil, nil, "docker", false, nil, 0)
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})
	post := func(path string, values url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ path, value string }{{"/variables", "shared"}, {"/stacks/app/variables", "local"}} {
		w := post(tc.path, url.Values{"name": {"DOMAIN"}, "value": {tc.value}, "action": {"save"}})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("save %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	got, err := v.Effective("app")
	if err != nil || got["BRINE_VAR_DOMAIN"] != "local" {
		t.Fatalf("override %v %v", got, err)
	}
	if w := post("/variables", url.Values{"name": {"REGION"}, "value": {"north"}, "action": {"save"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("shared save: %d", w.Code)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stacks/app", nil))
	for _, want := range []string{"BRINE_VAR_DOMAIN", "local", "Stack override", "Shared value: shared", "BRINE_VAR_REGION", "north", "Inherited from shared", "Manage shared variables"} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("stack page missing %q: %d %s", want, w.Code, w.Body.String())
		}
	}
	body := w.Body.String()
	if strings.Index(body, "BRINE_VAR_REGION") >= strings.Index(body, "BRINE_VAR_DOMAIN") {
		t.Fatal("inherited variables should precede stack variables")
	}
	if !strings.Contains(body, `aria-label="Inherited value for REGION"`) || !strings.Contains(body, `Inherited value is disabled.`) {
		t.Fatal("inherited disabled control missing")
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/variables", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "BRINE_VAR_DOMAIN") || !strings.Contains(w.Body.String(), `href="/variables" aria-current="page"`) {
		t.Fatalf("shared page: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings/variables", nil))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/variables" {
		t.Fatalf("old settings route: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w = post("/stacks/app/variables", url.Values{"name": {"invalid-name"}, "value": {"x"}, "action": {"save"}}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "name must start") || !strings.Contains(w.Body.String(), "BRINE_VAR_REGION") {
		t.Fatalf("invalid stack variable: %d", w.Code)
	}
	if w = post("/stacks/app/variables", url.Values{"name": {"DOMAIN"}, "action": {"delete"}}); w.Code != 303 {
		t.Fatal(w.Code)
	}
	got, _ = v.Effective("app")
	if got["BRINE_VAR_DOMAIN"] != "shared" {
		t.Fatal("shared fallback missing")
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/stacks/app", nil))
	if !strings.Contains(w.Body.String(), "Inherited from shared") || !strings.Contains(w.Body.String(), "shared") {
		t.Fatal("removed override not shown as inherited")
	}
	runner.Profile = "alpha"
	w = post("/variables", url.Values{"name": {"X"}, "value": {"x"}, "action": {"save"}})
	if w.Code != 503 {
		t.Fatalf("alpha write allowed: %d", w.Code)
	}
}

func TestVariablesAuthAndCSRF(t *testing.T) {
	authn, err := auth.New(auth.Config{User: "admin", Password: "test-only-password"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(authn.Middleware)
	Mount(router, &Handler{auth: authn}, httpx.Timeouts{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/variables", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("unauthenticated status %d", w.Code)
	}
	form := url.Values{"user": {"admin"}, "password": {"test-only-password"}}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	authn.HandleLogin(loginResponse, login)
	cookies := loginResponse.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set session cookie")
	}
	post := httptest.NewRequest(http.MethodPost, "/variables", strings.NewReader("action=save&name=FOO&value=bar"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	router.ServeHTTP(w, post)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status %d", w.Code)
	}
}
