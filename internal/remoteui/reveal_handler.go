package remoteui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/targetregistry"
)

type revealApproval struct {
	record             targetregistry.Record
	container, request string
	session            [32]byte
	expires            time.Time
}
type RevealHandler struct {
	auth      *auth.Authenticator
	registry  *targetregistry.Registry
	executor  *remotereveal.Executor
	mu        sync.Mutex
	key       [32]byte
	approvals map[string]revealApproval
	now       func() time.Time
}

// NewRevealHandler is intentionally unmounted. Callers must use Protected and
// independently review all deployment and HTTP delivery assumptions first.
func NewRevealHandler(a *auth.Authenticator, r *targetregistry.Registry, e *remotereveal.Executor) (*RevealHandler, error) {
	if a == nil || !a.Enabled() || r == nil || e == nil || e.Registry != r || e.Audit == nil || e.Signer == nil || e.Open == nil {
		return nil, errors.New("reveal unavailable")
	}
	h := &RevealHandler{auth: a, registry: r, executor: e, approvals: make(map[string]revealApproval), now: time.Now}
	if _, err := rand.Read(h.key[:]); err != nil {
		return nil, errors.New("reveal unavailable")
	}
	return h, nil
}
func (h *RevealHandler) session(cookie string) [32]byte {
	m := hmac.New(sha256.New, h.key[:])
	_, _ = m.Write([]byte(cookie))
	var v [32]byte
	copy(v[:], m.Sum(nil))
	return v
}
func (h *RevealHandler) issue(cookie string, a revealApproval) (string, error) {
	var b [32]byte
	if cookie == "" {
		return "", errors.New("session unavailable")
	}
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	a.session = h.session(cookie)
	a.expires = h.now().Add(2 * time.Minute)
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.approvals {
		if !h.now().Before(v.expires) {
			delete(h.approvals, k)
		}
	}
	if len(h.approvals) >= 1000 {
		return "", errors.New("capacity")
	}
	if _, ok := h.approvals[token]; ok {
		return "", errors.New("collision")
	}
	h.approvals[token] = a
	return token, nil
}
func (h *RevealHandler) consume(cookie, token string) (revealApproval, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, ok := h.approvals[token]
	s := h.session(cookie)
	if cookie == "" || !ok || !hmac.Equal(s[:], a.session[:]) {
		return revealApproval{}, false
	}
	delete(h.approvals, token)
	if !h.now().Before(a.expires) {
		return revealApproval{}, false
	}
	return a, true
}
func (h *RevealHandler) Protected() http.Handler {
	inner := h.auth.Middleware(http.HandlerFunc(h.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		}
		inner.ServeHTTP(w, r)
	})
}

var revealReviewTemplate = template.Must(template.New("review").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Review environment reveal</title></head><body><h1>Review environment reveal</h1><p>Current signed-in administrator session. This reveals secret environment values. Other metadata and logs may also contain secrets.</p><p>Full container ID: <code>{{.Container}}</code></p><p>Request ID: <code>{{.Request}}</code></p><h2>Complete current target record</h2><dl>{{range .Fields}}<dt>{{.Name}}</dt><dd><code>{{.Value}}</code></dd>{{end}}</dl><p>Approval expires after two minutes and can be used once. Logout or expiry cancels work where possible; requests and bytes already sent cannot be recalled.</p><button id="approve" type="button">Reveal values</button><button id="clear" type="button">Clear</button><pre id="values"></pre><script nonce="{{.Nonce}}">
const out=document.getElementById('values'), button=document.getElementById('approve'); let poll, expiry, generation=0, controller;
function clearValues(){generation++; if(controller)controller.abort(); controller=null; out.textContent=''; clearInterval(poll); clearTimeout(expiry)}; document.getElementById('clear').onclick=clearValues; document.addEventListener('visibilitychange',()=>{if(document.hidden)clearValues()});
function left(d){return Math.min(d.mono-performance.now(),d.wall-Date.now())}
async function status(signal){const mono=performance.now(), wall=Date.now(); const r=await fetch('/remote/session',{credentials:'same-origin',cache:'no-store',signal}); if(!r.ok) throw Error(); const s=await r.json(); if(!Number.isFinite(s.remainingMs)) throw Error(); const deadline={mono:mono+s.remainingMs-1000,wall:wall+s.remainingMs-1000}; if(left(deadline)<=0) throw Error(); return deadline}
button.onclick=async()=>{button.disabled=true; const current=++generation; controller=new AbortController(); const signal=controller.signal; try { const body=new URLSearchParams({_csrf:{{.CSRF}},token:{{.Token}},confirm:'yes'}); const r=await fetch('/remote/reveal/approve',{method:'POST',body,credentials:'same-origin',cache:'no-store',signal}); if(!r.ok) throw Error(); const v=await r.text(); let deadline=await status(signal); if(current!==generation||signal.aborted||document.hidden) return; deadline={mono:Math.min(deadline.mono,performance.now()+30000),wall:Math.min(deadline.wall,Date.now()+30000)}; let remaining=left(deadline); if(remaining<=0) throw Error(); out.textContent=v; remaining=left(deadline); if(remaining<=0){clearValues(); return} expiry=setTimeout(clearValues,remaining); poll=setInterval(async()=>{try{if(left(deadline)<=0){clearValues();return} const next=await status(signal); if(current!==generation||signal.aborted)return; deadline={mono:Math.min(deadline.mono,next.mono),wall:Math.min(deadline.wall,next.wall)}; const remaining=left(deadline); if(remaining<=0){clearValues();return} clearTimeout(expiry); expiry=setTimeout(clearValues,remaining)}catch(e){if(current===generation)clearValues()}},2000); } catch(e) {if(current===generation){clearValues(); out.textContent='Reveal unavailable';}} };
</script></body></html>`))

type revealReviewPage struct {
	Container, Request, CSRF, Token, Nonce string
	Fields                                 []enrollmentField
}

func (h *RevealHandler) serve(w http.ResponseWriter, r *http.Request) {
	if !auth.CookieAuthenticated(r) || strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		http.Error(w, "Interactive admin session required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/remote/reveal/review" && r.URL.Path != "/remote/reveal/approve") {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	count := 0
	for _, c := range r.Cookies() {
		if c.Name == auth.SessionCookie {
			count++
		}
	}
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil || cookie.Value == "" || count != 1 {
		http.Error(w, "Session unavailable", http.StatusForbidden)
		return
	}
	ctx, cancel, ok := h.auth.SessionContext(r)
	if !ok {
		http.Error(w, "Session unavailable", http.StatusForbidden)
		return
	}
	// Execute owns the cancellation function on the approval path.
	owned := false
	defer func() {
		if !owned {
			cancel()
		}
	}()
	if r.URL.Path == "/remote/reveal/review" {
		if len(r.PostForm) != 3 || len(r.PostForm["_csrf"]) != 1 || len(r.PostForm["host"]) != 1 || len(r.PostForm["container"]) != 1 || !fullID(r.PostForm.Get("container")) {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		record, err := h.registry.GetContext(ctx, r.PostForm.Get("host"))
		if err != nil || ctx.Err() != nil {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		request, err := agentclient.NewRequestID()
		if err != nil {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		a := revealApproval{record: record, container: r.PostForm.Get("container"), request: request}
		token, err := h.issue(cookie.Value, a)
		if err != nil || ctx.Err() != nil {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		var nonceBytes [24]byte
		if _, err := rand.Read(nonceBytes[:]); err != nil {
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(nonceBytes[:])
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = revealReviewTemplate.Execute(&sessionWriter{ResponseWriter: w, ctx: ctx}, revealReviewPage{a.container, a.request, h.auth.CSRFFor(r), token, nonce, displayEnrollmentFields(record)})
		return
	}
	if len(r.PostForm) != 3 || len(r.PostForm["_csrf"]) != 1 || len(r.PostForm["token"]) != 1 || len(r.PostForm["confirm"]) != 1 || r.PostForm.Get("confirm") != "yes" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	a, ok := h.consume(cookie.Value, r.PostForm.Get("token"))
	if !ok || ctx.Err() != nil {
		http.Error(w, "Review unavailable", http.StatusForbidden)
		return
	}
	controller := http.NewResponseController(w)
	if controller.SetWriteDeadline(time.Time{}) != nil {
		http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
		return
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()); close(done) })
	poison := false
	stopped := false
	stopWatcher := func() {
		if !stopped {
			if !stop() {
				<-done
			}
			stopped = true
		}
	}
	defer func() {
		stopWatcher()
		if !poison && ctx.Err() == nil {
			_ = controller.SetWriteDeadline(time.Time{})
		}
	}()
	started := false
	owned = true
	err = h.executor.Execute(remotereveal.Submission{Context: ctx, Release: cancel, AdminID: auth.Identity(r), RequestID: a.request}, a.record, a.container, func(c context.Context, result agentclient.RevealResult) error {
		if c.Err() != nil {
			return errors.New("cancelled")
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > 512<<10 || c.Err() != nil {
			return errors.New("delivery unavailable")
		}
		w.Header().Set("Content-Type", "application/json")
		started = true
		sw := &sessionWriter{ResponseWriter: w, ctx: c}
		n, err := sw.Write(data)
		if err != nil || n != len(data) || c.Err() != nil {
			poison = true
			_ = controller.SetWriteDeadline(time.Now())
			return errors.New("delivery unavailable")
		}
		if err = controller.Flush(); err != nil {
			poison = true
			_ = controller.SetWriteDeadline(time.Now())
			return errors.New("delivery unavailable")
		}
		return c.Err()
	})
	if err != nil && !started && !poison {
		// Execute releases its session context on every path. Stop its write
		// watcher before sending a fixed, non-secret failure response. Never
		// send a second response after delivery has begun.
		stopWatcher()
		if r.Context().Err() == nil {
			_ = controller.SetWriteDeadline(time.Time{})
			http.Error(w, "Reveal unavailable", http.StatusServiceUnavailable)
		}
	}
}
