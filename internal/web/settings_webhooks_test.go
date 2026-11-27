package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/webhooks"
)

type hooksFixtureDispatcher struct{}

func (hooksFixtureDispatcher) Validate(context.Context, webhooks.Trigger) error { return nil }
func (hooksFixtureDispatcher) Execute(context.Context, webhooks.Trigger, webhooks.IncomingRequest) webhooks.TriggerResult {
	return webhooks.TriggerResult{Status: "unknown", Outcome: "unknown"}
}
func TestWebhooksManagementSessionCSRFAndSecrecy(t *testing.T) {
	authn, e := auth.New(auth.Config{User: "admin", Password: "fixture-only-password", APIToken: "fixture-only-token"})
	if e != nil {
		t.Fatal(e)
	}
	svc, e := webhooks.Open(webhooks.Config{Directory: filepath.Join(t.TempDir(), "hooks"), EncryptionKey: bytes.Repeat([]byte{1}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	h := &Handler{auth: authn}
	h.EnableWebhooks(svc, hooksFixtureDispatcher{})
	r := chi.NewRouter()
	r.Use(authn.Middleware)
	r.Post("/login", authn.HandleLogin)
	api.Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, 0)
	Mount(r, h, httpx.Timeouts{})
	login := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"user": {"admin"}, "password": {"fixture-only-password"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, login)
	cookies := lw.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login cookie missing")
	}
	page := httptest.NewRequest("GET", "/settings/webhooks", nil)
	for _, c := range cookies {
		page.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, page)
	if pw.Code != 200 || !strings.Contains(pw.Body.String(), "hooks-view") {
		t.Fatalf("page: %d", pw.Code)
	}
	match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindStringSubmatch(pw.Body.String())
	if len(match) != 2 {
		t.Fatal("CSRF meta missing")
	}
	csrf := match[1]
	call := func(method, path, body, mode string, token bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if mode == "cookie" {
			for _, c := range cookies {
				req.AddCookie(c)
			}
		}
		if mode == "bearer" {
			req.Header.Set("Authorization", "Bearer fixture-only-token")
		}
		if token {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	payload := `{"name":"Fixture endpoint","url":"https://hooks.example.com/events","events":["operation.completed"],"stacks":[],"enabled":true}`
	if w := call("POST", "/api/webhooks/endpoints", payload, "cookie", false); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	if w := call("POST", "/api/webhooks/endpoints", payload, "bearer", false); w.Code != 403 {
		t.Fatal("bearer management allowed", w.Code)
	}
	w := call("POST", "/api/webhooks/endpoints", payload, "cookie", true)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Endpoint webhooks.Endpoint `json:"endpoint"`
		Secret   string            `json:"secret"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil || len(created.Secret) != 64 {
		t.Fatal("one-time key missing")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret cacheable")
	}
	for _, mode := range []string{"cookie", "bearer"} {
		w = call("GET", "/api/webhooks/", "", mode, false)
		if w.Code != 200 || strings.Contains(w.Body.String(), created.Secret) || strings.Contains(w.Body.String(), `"secret"`) {
			t.Fatalf("inventory leaked or failed: %d", w.Code)
		}
		w = call("GET", "/api/webhooks/requests?unresolved=1", "", mode, false)
		var page struct {
			CanManage bool                       `json:"can_manage"`
			Requests  []webhooks.IncomingRequest `json:"requests"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil || w.Code != 200 || page.CanManage != (mode == "cookie") || page.Requests == nil {
			t.Fatal("independent recovery permissions", w.Code, e, page.CanManage)
		}
	}
	w = call("POST", "/api/webhooks/endpoints/"+created.Endpoint.ID+"/rotate", `{"version":1}`, "cookie", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatal("rotation route not recognized", w.Code, w.Body.String())
	}
	w = call("POST", "/api/webhooks/endpoints/"+created.Endpoint.ID+"/test", `{}`, "cookie", true)
	if w.Code != 202 {
		t.Fatal("test route", w.Code, w.Body.String())
	}
	w = call("POST", "/api/webhooks/endpoints", `{"unknown":true}`, "cookie", true)
	if w.Code != 400 {
		t.Fatal("unknown JSON accepted")
	}
	w = call("POST", "/api/webhooks/endpoints", payload+`{}`, "cookie", true)
	if w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	w = call("GET", "/api/webhooks/", "", "", false)
	if w.Code != 401 {
		t.Fatal("unauthenticated read", w.Code)
	}
}
func TestWebhooksUnavailableDoesNotBecomeEmpty(t *testing.T) {
	a, e := auth.New(auth.Config{User: "admin", Password: "fixture-only-password", APIToken: "fixture-token"})
	if e != nil {
		t.Fatal(e)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, &Handler{auth: a}, httpx.Timeouts{})
	req := httptest.NewRequest(http.MethodGet, "/api/webhooks/", nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 || strings.Contains(w.Body.String(), `"endpoints":[]`) {
		t.Fatal("missing service became healthy empty", w.Code)
	}
}
