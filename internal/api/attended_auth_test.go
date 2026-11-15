package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
)

func TestAttendedCookieCSRFAndAuthenticatedPreview(t *testing.T) {
	_, _, _, h := fakeFlow(t)
	a, err := auth.New(auth.Config{User: "operator", Password: "password", APIToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"user": {"operator"}, "password": {"password"}}
	login := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lr := httptest.NewRecorder()
	a.HandleLogin(lr, login)
	if lr.Code != 303 || len(lr.Result().Cookies()) == 0 {
		t.Fatal("login failed", lr.Code)
	}
	cookie := lr.Result().Cookies()[0]
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Get("/csrf", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })
	r.Post("/api/stacks/{name}/operations/preview", h.attendedPreview)
	r.Post("/api/stacks/{name}/up", h.up)
	get := httptest.NewRequest("GET", "/csrf", nil)
	get.AddCookie(cookie)
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, get)
	csrf := cw.Body.String()
	if csrf == "" {
		t.Fatal("no CSRF")
	}
	post := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"action":"up"}`))
		req.AddCookie(cookie)
		if token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/api/stacks/synthetic/operations/preview", "/api/stacks/synthetic/up"} {
		if w := post(path, ""); w.Code != 403 {
			t.Fatalf("missing csrf %s: %d", path, w.Code)
		}
		if w := post(path, "incorrect"); w.Code != 403 {
			t.Fatalf("wrong csrf %s: %d", path, w.Code)
		}
	}
	w := post("/api/stacks/synthetic/operations/preview", csrf)
	if w.Code != 200 {
		t.Fatalf("csrf preview: %d %s", w.Code, w.Body.String())
	}
	var review struct {
		ID     string `json:"requestId"`
		Digest string `json:"reviewDigest"`
		Token  string `json:"confirmationToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"requestId": review.ID, "reviewDigest": review.Digest, "confirmationToken": review.Token})
	req := httptest.NewRequest("POST", "/api/stacks/synthetic/up", strings.NewReader(string(body)))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	confirmed := httptest.NewRecorder()
	r.ServeHTTP(confirmed, req)
	if confirmed.Code != 200 {
		t.Fatalf("csrf confirmation: %d %s", confirmed.Code, confirmed.Body.String())
	}
}
