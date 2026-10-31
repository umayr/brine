package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestAuth(t *testing.T, cfg Config) *Authenticator {
	t.Helper()
	if cfg.User == "" {
		cfg.User = "admin"
	}
	if cfg.Password == "" {
		cfg.Password = "secret"
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = time.Hour
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.delay = 0
	return a
}

func protectedHandler(a *Authenticator) http.Handler {
	return a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok:%s", a.User(r))
	}))
}

func doLogin(t *testing.T, a *Authenticator, user, password, next string) *http.Response {
	t.Helper()
	form := url.Values{"user": {user}, "password": {password}}
	if next != "" {
		form.Set("next", next)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.HandleLogin(rec, req)
	return rec.Result()
}

func sessionFor(t *testing.T, a *Authenticator, c *http.Cookie) *session {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[c.Value]
	if s == nil {
		t.Fatal("session not found")
	}
	return s
}

func TestMiddlewareUnauthenticated(t *testing.T) {
	a := newTestAuth(t, Config{})
	h := protectedHandler(a)

	// API request → JSON 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stacks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("API status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("API content type = %q", ct)
	}

	// Page request → redirect to login with next.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stacks", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("page status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login?next=") {
		t.Errorf("Location = %q", loc)
	}

	// HTMX request → HX-Redirect.
	req := httptest.NewRequest(http.MethodGet, "/web/stacks", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("HTMX status=%d HX-Redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}

	// Mutation without a session → 401.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stacks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST status = %d, want 401", rec.Code)
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	a := newTestAuth(t, Config{})
	h := protectedHandler(a)

	res := doLogin(t, a, "admin", "wrong", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", res.StatusCode)
	}
	if len(res.Cookies()) != 0 {
		t.Fatal("wrong password set a cookie")
	}

	res = doLogin(t, a, "admin", "secret", "/stacks")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/stacks" {
		t.Errorf("login redirect = %q, want /stacks", loc)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookie || !cookies[0].HttpOnly {
		t.Fatalf("session cookie = %+v", cookies)
	}

	// Authenticated request carries the user.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok:admin" {
		t.Fatalf("authenticated request = %d %q", rec.Code, rec.Body.String())
	}

	// Logout clears the cookie and invalidates the session.
	rec = httptest.NewRecorder()
	a.HandleLogout(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("logout status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("request after logout = %d, want 302 to login", rec.Code)
	}
}

func TestCSRF(t *testing.T) {
	a := newTestAuth(t, Config{})
	h := protectedHandler(a)

	res := doLogin(t, a, "admin", "secret", "")
	cookie := res.Cookies()[0]
	sess := sessionFor(t, a, cookie)

	// POST with a cookie but no token → 403.
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF = %d, want 403", rec.Code)
	}

	// Header token → allowed.
	req = httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.AddCookie(cookie)
	req.Header.Set(csrfHeader, sess.csrf)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST with CSRF header = %d, want 200", rec.Code)
	}

	// Form field token → allowed.
	form := url.Values{csrfField: {sess.csrf}}.Encode()
	req = httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(form))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST with CSRF form field = %d, want 200", rec.Code)
	}

	// GET never needs CSRF.
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET with cookie = %d, want 200", rec.Code)
	}
}

func TestBearerToken(t *testing.T) {
	a := newTestAuth(t, Config{APIToken: "tok"})
	h := protectedHandler(a)

	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer POST = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer nope")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad bearer = %d, want 401", rec.Code)
	}
}

func TestNoAuth(t *testing.T) {
	a := newTestAuth(t, Config{NoAuth: true})
	h := protectedHandler(a)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stacks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-auth POST = %d, want 200", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	a := newTestAuth(t, Config{SessionTTL: time.Minute})
	h := protectedHandler(a)
	cookie := doLogin(t, a, "admin", "secret", "").Cookies()[0]

	a.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("expired session = %d, want 302 to login", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newTestAuth(t, Config{})
	for i := 0; i < maxAttempts; i++ {
		if res := doLogin(t, a, "admin", "wrong", ""); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d", i, res.StatusCode)
		}
	}
	if res := doLogin(t, a, "admin", "wrong", ""); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("rate limited status = %d, want 429", res.StatusCode)
	}
	// A correct password is also blocked while the window is open.
	if res := doLogin(t, a, "admin", "secret", ""); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("rate limited correct login = %d, want 429", res.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/stacks":             "/stacks",
		"/stacks?x=1":         "/stacks?x=1",
		"":                    "/",
		"//evil.example":      "/",
		"https://evil.io":     "/",
		"../etc/passwd":       "/",
		"javascript:alert(1)": "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadOrCreateCredentials(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	creds, created, path, err := LoadOrCreate(dataDir, "", "", "")
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !created {
		t.Error("expected credentials to be created")
	}
	if creds.User != "admin" || len(creds.Password) < 20 || len(creds.APIToken) < 20 {
		t.Fatalf("generated credentials look wrong: %+v", creds)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials mode = %o, want 600", perm)
	}
	data, _ := os.ReadFile(path)
	var onDisk Credentials
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("credentials file is not JSON: %v", err)
	}
	if onDisk.Password != creds.Password || onDisk.APIToken != creds.APIToken {
		t.Error("persisted credentials differ from returned ones")
	}

	// Second call reuses the file.
	again, created, _, err := LoadOrCreate(dataDir, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created || again.Password != creds.Password || again.APIToken != creds.APIToken {
		t.Errorf("second LoadOrCreate = %+v created=%v", again, created)
	}

	// Explicit credentials do not need a file.
	explicitDir := filepath.Join(t.TempDir(), "data")
	c, created, p, err := LoadOrCreate(explicitDir, "me", "pw", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if created || p != "" || c.User != "me" || c.Password != "pw" || c.APIToken != "tok" {
		t.Errorf("explicit creds = %+v created=%v path=%q", c, created, p)
	}
}
