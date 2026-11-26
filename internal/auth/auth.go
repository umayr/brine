// Package auth provides Brine's single-admin authentication: a login form with
// server-side sessions, CSRF protection for cookie-authenticated requests, and
// a bearer token for API clients.
package auth

import (
	"container/heap"
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
	// mu protects the live login, token, file digest, and sessions.
	login      loginCredential
	tokenHash  [sha256.Size]byte
	hasToken   bool
	management *credentialManagement

	noAuth bool
	secure bool
	ttl    time.Duration

	delay time.Duration // failed-login delay (tests set this to 0)
	now   func() time.Time

	mu          sync.Mutex
	sessions    map[string]*session
	attemptMu   sync.Mutex // independent of session operations
	attempts    map[string]*attempt
	expirations attemptHeap
	derivations chan struct{} // bounds concurrent password checks across all clients
}

type session struct {
	// submission serializes logout with the start and completion of an authorized transfer.
	submission chan struct{}
	done       chan struct{} // closed under a.mu when the session is revoked
	user       string
	csrf       string
	expires    time.Time
}

type attempt struct {
	ip    string
	count int
	reset time.Time
	index int
}

// attemptHeap contains exactly one entry per address, ordered by expiry.
type attemptHeap []*attempt

func (h attemptHeap) Len() int           { return len(h) }
func (h attemptHeap) Less(i, j int) bool { return h[i].reset.Before(h[j].reset) }
func (h attemptHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *attemptHeap) Push(x any) {
	att := x.(*attempt)
	att.index = len(*h)
	*h = append(*h, att)
}
func (h *attemptHeap) Pop() any {
	old := *h
	n := len(old)
	att := old[n-1]
	old[n-1] = nil
	att.index = -1
	*h = old[:n-1]
	return att
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
		noAuth:      cfg.NoAuth,
		secure:      cfg.SecureCookies,
		ttl:         cfg.SessionTTL,
		delay:       250 * time.Millisecond,
		now:         time.Now,
		sessions:    make(map[string]*session),
		attempts:    make(map[string]*attempt),
		derivations: make(chan struct{}, maxConcurrentDerivations),
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
	login := loginCredential{user: cfg.User}
	if err := login.setPassword(cfg.Password); err != nil {
		return nil, err
	}
	a.login = login

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
			if !localDevRequest(r) {
				http.Error(w, "no-auth requests require a local host and same origin", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), requestInfo{method: "none", user: "dev"})))
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if token := bearerToken(r); token != "" {
			user, valid := a.tokenIdentity(token)
			if !valid {
				writeJSONError(w, http.StatusUnauthorized, "invalid API token")
				return
			}
			next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), requestInfo{method: "bearer", user: user})))
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

// localDevRequest limits browser access in no-auth mode. Loopback binding alone
// does not prevent DNS rebinding or cross-origin requests from a victim browser.
// This is defense in depth, not a replacement for authentication or a proxy ACL.
func localDevRequest(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.Trim(host, "[]")
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}

// Authenticated reports an authenticated session or bearer token, excluding no-auth mode.
func Authenticated(r *http.Request) bool {
	info, ok := r.Context().Value(ctxKey{}).(requestInfo)
	return ok && (info.method == "cookie" || info.method == "bearer")
}

// CookieAuthenticated reports whether middleware authenticated this request by cookie.
func CookieAuthenticated(r *http.Request) bool {
	info, ok := r.Context().Value(ctxKey{}).(requestInfo)
	return ok && info.method == "cookie" && info.csrf != ""
}

// Identity returns only the trusted authenticated account name, never credentials.
func Identity(r *http.Request) string {
	if !Authenticated(r) {
		return ""
	}
	info, _ := r.Context().Value(ctxKey{}).(requestInfo)
	return info.user
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
	// Reject overload instead of accumulating unbounded waiting requests. Acquire
	// before admission so a busy server does not consume a client's budget.
	select {
	case a.derivations <- struct{}{}:
	default:
		http.Error(w, "login temporarily busy, try again later", http.StatusServiceUnavailable)
		return
	}
	ip := clientIP(r)
	if !a.allowAttempt(ip) {
		<-a.derivations
		http.Error(w, "too many login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	user := r.PostFormValue("user")
	password := r.PostFormValue("password")
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	valid := func() bool {
		defer func() { <-a.derivations }()
		return login.verify(user, password)
	}()
	if !valid {
		if a.delay > 0 {
			time.Sleep(a.delay)
		}
		a.renderLogin(w, r, "Invalid username or password.", http.StatusUnauthorized)
		return
	}

	sessID, err := a.createSession(login)
	if err == errLoginChanged {
		a.renderLogin(w, r, "Sign-in changed. Please try again.", http.StatusUnauthorized)
		return
	}
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
}

// SubmissionLease revalidates the cookie session and serializes a submission
// with logout. The caller must release it promptly after the network operation.
// If logout wins first, acquisition fails. If submission wins, logout waits;
// a network request already started cannot be recalled. No session identifier
// is returned. A deadline prevents starting work after session expiry.
// SessionContext is a revocable lifetime for an authenticated cookie request.
// Unlike SubmissionLease it does not delay logout. Logout and expiration cancel
// the context; already written or buffered response bytes cannot be recalled.
func (a *Authenticator) SessionContext(r *http.Request) (context.Context, func(), bool) {
	if a == nil || !a.Enabled() {
		return nil, nil, false
	}
	info, ok := r.Context().Value(ctxKey{}).(requestInfo)
	if !ok || info.method != "cookie" || info.csrf == "" {
		return nil, nil, false
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil, false
	}
	a.mu.Lock()
	sess := a.sessions[cookie.Value]
	valid := sess != nil && a.now().Before(sess.expires) && subtle.ConstantTimeCompare([]byte(info.csrf), []byte(sess.csrf)) == 1 && r.Context().Err() == nil
	if !valid {
		a.mu.Unlock()
		return nil, nil, false
	}
	ctx, cancel := context.WithDeadline(r.Context(), sess.expires)
	done := sess.done
	a.mu.Unlock()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel, true
}

func (a *Authenticator) SubmissionLease(r *http.Request) (context.Context, func(), bool) {
	if a == nil || !a.Enabled() {
		return nil, nil, false
	}
	info, ok := r.Context().Value(ctxKey{}).(requestInfo)
	if !ok || info.method != "cookie" || info.csrf == "" {
		return nil, nil, false
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, nil, false
	}
	a.mu.Lock()
	sess := a.sessions[cookie.Value]
	a.mu.Unlock()
	if sess == nil {
		return nil, nil, false
	}
	select {
	case <-r.Context().Done():
		return nil, nil, false
	case sess.submission <- struct{}{}:
	}
	a.mu.Lock()
	valid := r.Context().Err() == nil && a.sessions[cookie.Value] == sess && a.now().Before(sess.expires) && subtle.ConstantTimeCompare([]byte(info.csrf), []byte(sess.csrf)) == 1
	a.mu.Unlock()
	if !valid {
		<-sess.submission
		return nil, nil, false
	}
	ctx, cancel := context.WithDeadline(r.Context(), sess.expires)
	return ctx, func() { cancel(); <-sess.submission }, true
}

// HandleLogout clears the session and redirects to the login page.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if a != nil {
		if c, err := r.Cookie(SessionCookie); err == nil {
			a.mu.Lock()
			sess := a.sessions[c.Value]
			a.mu.Unlock()
			if sess != nil {
				select {
				case <-r.Context().Done():
					http.Error(w, "Logout cancelled; session remains active", http.StatusRequestTimeout)
					return
				case sess.submission <- struct{}{}:
				}
				if r.Context().Err() != nil {
					<-sess.submission
					http.Error(w, "Logout cancelled; session remains active", http.StatusRequestTimeout)
					return
				}
				a.mu.Lock()
				if r.Context().Err() != nil {
					a.mu.Unlock()
					<-sess.submission
					http.Error(w, "Logout cancelled; session remains active", http.StatusRequestTimeout)
					return
				}
				if a.sessions[c.Value] == sess {
					delete(a.sessions, c.Value)
					close(sess.done)
				}
				a.mu.Unlock()
				<-sess.submission
			}
		}
	}
	a.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ClearSessionCookie removes the browser's cookie after server-side revocation.
func (a *Authenticator) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Path:     "/",
		HttpOnly: true,
		Secure:   a != nil && a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a *Authenticator) renderLogin(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	a.mu.Lock()
	user := a.login.user
	a.mu.Unlock()
	data := struct {
		Error string
		Next  string
		User  string
	}{
		Error: errMsg,
		Next:  safeNext(r.URL.Query().Get("next")),
		User:  user,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, data)
}

// A snapshot's slices are read-only; password updates replace rather than
// modify them. The revision binds a verified login to session creation.
type loginCredential struct {
	user          string
	salt, passKey []byte
	revision      uint64
}

func (c *loginCredential) setPassword(password string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyLength)
	if err != nil {
		return fmt.Errorf("derive password key: %w", err)
	}
	c.salt, c.passKey = salt, key
	return nil
}

func (c loginCredential) verify(user, password string) bool {
	// Always derive a key so that a wrong username does not return faster.
	candidate, err := pbkdf2.Key(sha256.New, password, c.salt, passwordIterations, passwordKeyLength)
	if err != nil {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(c.user)) == 1
	passOK := subtle.ConstantTimeCompare(candidate, c.passKey) == 1
	return userOK && passOK
}

func (a *Authenticator) tokenIdentity(token string) (string, bool) {
	got := sha256.Sum256([]byte(token))
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.login.user, a.hasToken && subtle.ConstantTimeCompare(got[:], a.tokenHash[:]) == 1
}

func (a *Authenticator) createSession(login loginCredential) (string, error) {
	idRaw := make([]byte, 32)
	if _, err := rand.Read(idRaw); err != nil {
		return "", err
	}
	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(idRaw)
	sess := &session{
		submission: make(chan struct{}, 1),
		done:       make(chan struct{}),
		user:       login.user,
		csrf:       base64.RawURLEncoding.EncodeToString(csrfRaw),
		expires:    a.now().Add(a.ttl),
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.login.revision != login.revision {
		return "", errLoginChanged
	}
	a.pruneLocked()
	a.sessions[id] = sess
	return id, nil
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
		close(sess.done)
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
			close(sess.done)
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

const (
	maxAttempts              = 20
	attemptWindow            = 5 * time.Minute
	maxAttemptIPs            = 10_000
	maxConcurrentDerivations = 4
)

// allowAttempt atomically reserves one attempt in a fixed window. Successful
// logins count too: clearing on success would let overlapping requests or a
// known password bypass the admission limit.
func (a *Authenticator) allowAttempt(ip string) bool {
	a.attemptMu.Lock()
	defer a.attemptMu.Unlock()
	now := a.now()
	att := a.attempts[ip]
	if att == nil {
		if len(a.attempts) >= maxAttemptIPs {
			// Reclaim only the slot needed for this request. In particular, do
			// not scan the whole table on every rejection during a live flood.
			if now.Before(a.expirations[0].reset) {
				return false // do not evict live limits under address churn
			}
			expired := heap.Pop(&a.expirations).(*attempt)
			delete(a.attempts, expired.ip)
		}
		att = &attempt{ip: ip, reset: now.Add(attemptWindow)}
		a.attempts[ip] = att
		heap.Push(&a.expirations, att)
	} else if !now.Before(att.reset) {
		att.count = 0
		att.reset = now.Add(attemptWindow)
		heap.Fix(&a.expirations, att.index)
	}
	if att.count >= maxAttempts {
		return false
	}
	att.count++
	return true
}

func isPublicPath(path string) bool {
	switch path {
	case "/login", "/api/health", "/favicon.ico", "/robots.txt":
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
<html lang="en" data-theme="dark">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sign in · Brine</title>
<script src="/static/theme.js"></script>
<link href="/static/theme.css" rel="stylesheet">
<link href="/static/vendor/app.css" rel="stylesheet">
</head>
<body class="login-page">
  <main class="login-card">
    <div class="login-brand"><span class="login-dot" aria-hidden="true">B</span><span>Brine / Control plane</span></div>
    <h1>Welcome back.</h1>
    <p class="login-intro">Sign in to manage your self-hosted services.</p>
    <form method="post" action="/login">
    {{if .Error}}<p class="login-error" role="alert">{{.Error}}</p>{{end}}
    <input type="hidden" name="next" value="{{.Next}}">
    <label for="user">Username</label>
    <input id="user" name="user" autocomplete="username" autofocus required value="{{.User}}">
    <label for="password">Password</label>
    <input id="password" name="password" type="password" autocomplete="current-password" required>
    <button type="submit">Sign in</button>
    </form>
    <p class="login-hint">First run? Credentials are in <span class="mono">~/.brine/credentials.json</span> (or your configured data directory).</p>
  </main>
</body>
</html>
`))
