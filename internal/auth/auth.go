// Package auth provides Brine's single-admin authentication: a login form with
// server-side sessions, CSRF protection for cookie-authenticated requests, and
// a bearer token for API clients.
package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// SessionCookie is the name of the session cookie.
	SessionCookie = "brine_session"
	csrfHeader    = "X-CSRF-Token"
	csrfField     = "_csrf"

	passwordIterations = 210_000
	passwordKeyLength  = 32
)

// Config configures an Authenticator.
type Config struct {
	User          string
	Password      string
	APIToken      string
	NoAuth        bool
	SecureCookies bool
	SessionTTL    time.Duration
}

// Authenticator validates credentials and guards HTTP handlers.
type Authenticator struct {
	user      string
	salt      []byte
	passKey   []byte
	tokenHash [sha256.Size]byte
	hasToken  bool
	noAuth    bool
	secure    bool
	ttl       time.Duration

	delay time.Duration // failed-login delay (tests set this to 0)
	now   func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
	attempts map[string]*attempt
}

type session struct {
	user    string
	csrf    string
	expires time.Time
}

type attempt struct {
	count int
	reset time.Time
}

type ctxKey struct{}

type requestInfo struct {
	method string // "cookie", "bearer" or "none"
	user   string
	csrf   string
}

// New creates an Authenticator. In NoAuth mode all requests are allowed and
// CSRF checks are disabled; use it only for local development.
func New(cfg Config) (*Authenticator, error) {
	a := &Authenticator{
		noAuth:   cfg.NoAuth,
		secure:   cfg.SecureCookies,
		ttl:      cfg.SessionTTL,
		delay:    250 * time.Millisecond,
		now:      time.Now,
		sessions: make(map[string]*session),
		attempts: make(map[string]*attempt),
	}
	if a.ttl <= 0 {
		a.ttl = 12 * time.Hour
	}
	if cfg.NoAuth {
		return a, nil
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, fmt.Errorf("auth user is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("auth password is required")
	}
	a.user = cfg.User

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, cfg.Password, salt, passwordIterations, passwordKeyLength)
	if err != nil {
		return nil, fmt.Errorf("derive password key: %w", err)
	}
	a.salt = salt
	a.passKey = key

	if cfg.APIToken != "" {
		a.tokenHash = sha256.Sum256([]byte(cfg.APIToken))
		a.hasToken = true
	}
	return a, nil
}

// Enabled reports whether authentication is active.
func (a *Authenticator) Enabled() bool { return a != nil && !a.noAuth }

// Middleware rejects unauthenticated requests, enforces CSRF for cookie
// sessions, and stores the authenticated identity in the request context.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || a.noAuth {
			next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), requestInfo{method: "none", user: "dev"})))
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if token := bearerToken(r); token != "" {
			if !a.validToken(token) {
				writeJSONError(w, http.StatusUnauthorized, "invalid API token")
				return
			}
			next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), requestInfo{method: "bearer", user: a.user})))
			return
		}

		sess := a.sessionFromRequest(r)
		if sess != nil {
			if requiresCSRF(r.Method) && !a.checkCSRF(r, sess) {
				writeJSONError(w, http.StatusForbidden, "invalid or missing CSRF token")
				return
			}
			next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), requestInfo{
				method: "cookie",
				user:   sess.user,
				csrf:   sess.csrf,
			})))
			return
		}

		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if wantsJSON(r) {
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// CSRFFor returns the CSRF token for the current session, or "" when the
// request was authenticated with a bearer token or auth is disabled.
func (a *Authenticator) CSRFFor(r *http.Request) string {
	if info, ok := r.Context().Value(ctxKey{}).(requestInfo); ok {
		return info.csrf
	}
	return ""
}

// User returns the authenticated user for the request.
func (a *Authenticator) User(r *http.Request) string {
	if info, ok := r.Context().Value(ctxKey{}).(requestInfo); ok {
		return info.user
	}
	return ""
}

// HandleLogin serves the login form (GET) and processes logins (POST).
func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.noAuth {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if a.sessionFromRequest(r) != nil {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		a.renderLogin(w, r, "", http.StatusOK)
	case http.MethodPost:
		a.handleLoginPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Authenticator) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if !a.allowAttempt(ip) {
		http.Error(w, "too many login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	user := r.PostFormValue("user")
	password := r.PostFormValue("password")
	if !a.verifyPassword(user, password) {
		a.recordFailure(ip)
		if a.delay > 0 {
			time.Sleep(a.delay)
		}
		a.renderLogin(w, r, "Invalid username or password.", http.StatusUnauthorized)
		return
	}

	a.clearAttempts(ip)
	sessID, sess, err := a.createSession()
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    sessID,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
	_ = sess
}

// HandleLogout clears the session and redirects to the login page.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if a != nil {
		if c, err := r.Cookie(SessionCookie); err == nil {
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a != nil && a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *Authenticator) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	data := struct {
		Error string
		Next  string
		User  string
	}{
		Error: errMsg,
		Next:  safeNext(r.URL.Query().Get("next")),
		User:  a.user,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, data)
}

func (a *Authenticator) verifyPassword(user, password string) bool {
	// Always derive a key so that a wrong username does not return faster.
	candidate, err := pbkdf2.Key(sha256.New, password, a.salt, passwordIterations, passwordKeyLength)
	if err != nil {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.user)) == 1
	passOK := subtle.ConstantTimeCompare(candidate, a.passKey) == 1
	return userOK && passOK
}

func (a *Authenticator) validToken(token string) bool {
	if !a.hasToken {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], a.tokenHash[:]) == 1
}

func (a *Authenticator) createSession() (string, *session, error) {
	idRaw := make([]byte, 32)
	if _, err := rand.Read(idRaw); err != nil {
		return "", nil, err
	}
	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return "", nil, err
	}
	id := base64.RawURLEncoding.EncodeToString(idRaw)
	sess := &session{
		user:    a.user,
		csrf:    base64.RawURLEncoding.EncodeToString(csrfRaw),
		expires: a.now().Add(a.ttl),
	}
	a.mu.Lock()
	a.pruneLocked()
	a.sessions[id] = sess
	a.mu.Unlock()
	return id, sess, nil
}

func (a *Authenticator) sessionFromRequest(r *http.Request) *session {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sess := a.sessions[c.Value]
	if sess == nil {
		return nil
	}
	if a.now().After(sess.expires) {
		delete(a.sessions, c.Value)
		return nil
	}
	return sess
}

// pruneLocked drops expired sessions. Callers must hold a.mu.
func (a *Authenticator) pruneLocked() {
	now := a.now()
	for id, sess := range a.sessions {
		if now.After(sess.expires) {
			delete(a.sessions, id)
		}
	}
}

func (a *Authenticator) checkCSRF(r *http.Request, sess *session) bool {
	token := r.Header.Get(csrfHeader)
	if token == "" && isFormContentType(r) {
		_ = r.ParseForm()
		token = r.PostFormValue(csrfField)
	}
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.csrf)) == 1
}

const maxAttempts = 10
const attemptWindow = 5 * time.Minute

func (a *Authenticator) allowAttempt(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	att := a.attempts[ip]
	if att == nil || now.After(att.reset) {
		return true
	}
	return att.count < maxAttempts
}

func (a *Authenticator) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	att := a.attempts[ip]
	if att == nil || now.After(att.reset) {
		att = &attempt{reset: now.Add(attemptWindow)}
		a.attempts[ip] = att
	}
	att.count++
	// Bound the map on hostile input.
	if len(a.attempts) > 10_000 {
		a.attempts = make(map[string]*attempt)
	}
}

func (a *Authenticator) clearAttempts(ip string) {
	a.mu.Lock()
	delete(a.attempts, ip)
	a.mu.Unlock()
}

func isPublicPath(path string) bool {
	switch path {
	case "/login", "/logout", "/api/health", "/favicon.ico", "/robots.txt":
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

func requiresCSRF(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func isFormContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
}

func safeNext(next string) string {
	if next == "" {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func withInfo(ctx context.Context, info requestInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var loginTemplate = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en" data-theme="night">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sign in · Brine</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
         background: #14161b; color: #e7e9ee; font-family: Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
  .card { width: 100%; max-width: 22rem; padding: 2rem; border: 1px solid #2d3138; border-radius: 0.75rem;
          background: #1a1d23; box-shadow: 0 24px 60px rgba(0,0,0,.45); }
  .brand { display: flex; align-items: center; gap: .6rem; margin-bottom: 1.5rem; }
  .dot { width: 1.9rem; height: 1.9rem; border-radius: .55rem; background: #8b5cf6;
         display: flex; align-items: center; justify-content: center; font-weight: 700; }
  h1 { font-size: 1rem; margin: 0; letter-spacing: -.01em; }
  label { display: block; font-size: .75rem; text-transform: uppercase; letter-spacing: .07em;
          color: #8b929e; margin: 0 0 .4rem; font-weight: 600; }
  input { width: 100%; padding: .6rem .7rem; margin-bottom: 1rem; border-radius: .45rem;
          border: 1px solid #333842; background: #14161b; color: #e7e9ee; font-size: .9rem; }
  input:focus { outline: 2px solid #8b5cf6; outline-offset: 1px; border-color: transparent; }
  button { width: 100%; padding: .65rem; border: 0; border-radius: .45rem; background: #8b5cf6;
           color: #fff; font-weight: 600; font-size: .9rem; cursor: pointer; }
  button:hover { background: #7c4deb; }
  .err { margin: 0 0 1rem; padding: .6rem .7rem; border-radius: .45rem;
         background: rgba(239,68,68,.12); border: 1px solid rgba(239,68,68,.35); color: #fca5a5; font-size: .8rem; }
  .hint { margin-top: 1.25rem; font-size: .72rem; color: #6b7280; line-height: 1.5; }
  .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
</head>
<body>
  <form class="card" method="post" action="/login">
    <div class="brand">
      <div class="dot">B</div>
      <h1>Sign in to Brine</h1>
    </div>
    {{if .Error}}<p class="err">{{.Error}}</p>{{end}}
    <input type="hidden" name="next" value="{{.Next}}">
    <label for="user">Username</label>
    <input id="user" name="user" autocomplete="username" autofocus required value="{{.User}}">
    <label for="password">Password</label>
    <input id="password" name="password" type="password" autocomplete="current-password" required>
    <button type="submit">Sign in</button>
    <p class="hint">First run? Credentials are in <span class="mono">~/.brine/credentials.json</span> (or were printed at startup).</p>
  </form>
</body>
</html>
`))
