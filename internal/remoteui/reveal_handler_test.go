package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

type reviewSigner struct{ key ed25519.PrivateKey }

func (s reviewSigner) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
func (s reviewSigner) Sign(ctx context.Context, b []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return ed25519.Sign(s.key, b), nil
}

type reviewAudit struct {
	begin, finish         atomic.Int32
	failBegin, failFinish atomic.Bool
}

func (a *reviewAudit) BeginRevealAudit(ctx context.Context, admin, target, id string) (revealaudit.Intent, error) {
	a.begin.Add(1)
	if a.failBegin.Load() {
		return revealaudit.Intent{}, errors.New("sensitive audit error")
	}
	return revealaudit.Intent{Correlation: "c"}, ctx.Err()
}
func (a *reviewAudit) Finish(ctx context.Context, id string, o revealaudit.Outcome) error {
	a.finish.Add(1)
	if a.failFinish.Load() {
		return errors.New("sensitive audit error")
	}
	return ctx.Err()
}

type reviewSession struct {
	calls   *atomic.Int32
	entered chan struct{}
	wait    bool
	grant   *agentclient.Grant
}

func (s *reviewSession) Epoch() string { return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }
func (s *reviewSession) Close() error  { return nil }
func (s *reviewSession) Reveal(ctx context.Context, epoch, id string, g agentclient.Grant) (agentclient.RevealResult, error) {
	s.calls.Add(1)
	*s.grant = g
	if s.entered != nil {
		close(s.entered)
	}
	if s.wait {
		<-ctx.Done()
		return agentclient.RevealResult{}, ctx.Err()
	}
	r := agentclient.RevealResult{ContainerID: id}
	r.Env = append(r.Env, struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{"KEY", "secret-value"})
	return r, nil
}

type blockedResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (w *blockedResponse) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		w.once.Do(func() { close(w.unblock) })
	}
	return nil
}
func (w *blockedResponse) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.unblock
	return 0, errors.New("write interrupted")
}
func (w *blockedResponse) FlushError() error { return nil }

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
func (w *deadlineRecorder) FlushError() error                { w.Flush(); return nil }

func TestRevealRealAuthSessionConfirmation(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", APIToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	rec.DaemonID = "daemon  x"
	pub := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	hash := sha256.Sum256(pub)
	rec.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	if err = reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	audit := &reviewAudit{}
	var calls atomic.Int32
	var grant agentclient.Grant
	block := false
	var entered chan struct{}
	e := &remotereveal.Executor{Registry: reg, Audit: audit, Signer: reviewSigner{ed25519.NewKeyFromSeed(make([]byte, 32))}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (remotereveal.Session, error) {
		return &reviewSession{calls: &calls, wait: block, entered: entered, grant: &grant}, nil
	}}
	noAuth, err := auth.New(auth.Config{NoAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRevealHandler(noAuth, reg, e); err == nil {
		t.Fatal("no-auth configuration accepted")
	}
	h, err := NewRevealHandler(a, reg, e)
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Protected()
	login := func() (*http.Cookie, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		a.HandleLogin(w, r)
		c := w.Result().Cookies()[0]
		w = httptest.NewRecorder()
		r = httptest.NewRequest("GET", "/csrf", nil)
		r.AddCookie(c)
		a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })).ServeHTTP(w, r)
		return c, w.Body.String()
	}
	c, csrf := login()
	c2, csrf2 := login()
	post := func(path string, v url.Values, c *http.Cookie, bearer bool) *deadlineRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c != nil {
			r.AddCookie(c)
		}
		if bearer {
			r.Header.Set("Authorization", "Bearer token")
		}
		w := &deadlineRecorder{httptest.NewRecorder()}
		handler.ServeHTTP(w, r)
		return w
	}
	id := strings.Repeat("a", 64)
	review := url.Values{"_csrf": {csrf}, "host": {rec.ID}, "container": {id}}
	if post("/remote/reveal/review", review, nil, false).Code == 200 || post("/remote/reveal/review", review, nil, true).Code == 200 {
		t.Fatal("unauthenticated accepted")
	}
	for _, v := range []url.Values{
		{"_csrf": {csrf}, "host": {rec.ID, rec.ID}, "container": {id}},
		{"_csrf": {csrf}, "host": {rec.ID}, "container": {id}, "extra": {"x"}},
		{"_csrf": {csrf, csrf}, "host": {rec.ID}, "container": {id}},
	} {
		if post("/remote/reveal/review", v, c, false).Code == http.StatusOK {
			t.Fatal("ambiguous review accepted")
		}
	}
	dupReq := httptest.NewRequest("POST", "/remote/reveal/review", strings.NewReader(review.Encode()))
	dupReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dupReq.AddCookie(c)
	dupReq.AddCookie(c2)
	dupOut := httptest.NewRecorder()
	handler.ServeHTTP(dupOut, dupReq)
	if dupOut.Code == http.StatusOK {
		t.Fatal("ambiguous cookies accepted")
	}
	bad := url.Values{"_csrf": {"bad"}, "host": {rec.ID}, "container": {id}}
	if post("/remote/reveal/review", bad, c, false).Code == 200 {
		t.Fatal("csrf accepted")
	}
	w := post("/remote/reveal/review", review, c, false)
	body := html.UnescapeString(w.Body.String())
	if w.Code != 200 || strings.Contains(body, c.Value) || !strings.Contains(body, id) {
		t.Fatal("invalid review", w.Code)
	}
	// Independently specify every expected field and adversarial spacing.
	expected := map[string]string{
		"id": `"server"`, "revision": `"1"`, "host": `"host.example"`, "port": `"22"`, "user": `"operator"`,
		"key_path": `"/home/operator/key"`, "host_public_key": `"` + strings.ReplaceAll(rec.HostPublicKey, " ", `\x20`) + `"`,
		"host_key_sha256": strconv.Quote(rec.HostKeySHA256), "agent_installation_id": strconv.Quote(rec.AgentInstallationID),
		"engine_id": `"11111111-1111-4111-8111-111111111111"`, "remote_socket": `"/run/agent.sock"`,
		"agent_public_key": strconv.Quote(rec.AgentPublicKey), "agent_fingerprint": strconv.Quote(rec.AgentFingerprint),
		"engine_kind": `"docker"`, "engine_socket": `"unix:///run/docker.sock"`, "daemon_id": `"daemon\x20\x20x"`,
		"generation": `"1"`, "reveal_key_fingerprint": strconv.Quote(rec.RevealKeyFingerprint),
	}
	for name, value := range expected {
		if !strings.Contains(body, "<dt>"+name+"</dt><dd><code>"+value+"</code></dd>") {
			t.Fatalf("missing exact field %s", name)
		}
	}
	if strings.Contains(fmt.Sprintf("%#v", h.approvals), c.Value) {
		t.Fatal("raw cookie retained in approval state")
	}
	h.mu.Lock()
	var token string
	var reviewedRequest string
	for k, v := range h.approvals {
		token = k
		reviewedRequest = v.request
	}
	h.mu.Unlock()
	if token == "" || !strings.Contains(body, reviewedRequest) {
		t.Fatal("missing approval or reviewed request ID")
	}
	approve := url.Values{"_csrf": {csrf2}, "token": {token}, "confirm": {"yes"}}
	if post("/remote/reveal/approve", approve, c2, false).Code != http.StatusForbidden {
		t.Fatal("cross-session accepted")
	}
	approve.Set("_csrf", csrf)
	results := make(chan *deadlineRecorder, 2)
	go func() { results <- post("/remote/reveal/approve", approve, c, false) }()
	go func() { results <- post("/remote/reveal/approve", approve, c, false) }()
	first, second := <-results, <-results
	if first.Code == http.StatusOK {
		w = first
	} else {
		w = second
	}
	if calls.Load() != 1 || audit.begin.Load() != 1 || audit.finish.Load() != 1 || grant.RequestID != reviewedRequest || grant.ContainerID != id || !strings.Contains(w.Body.String(), "secret-value") || (first.Code == http.StatusOK && second.Code == http.StatusOK) {
		t.Fatal("concurrent approval did not execute exactly once")
	}
	if post("/remote/reveal/approve", approve, c, false).Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatal("replay accepted")
	}
	// A reviewed record cannot be substituted after the review page.
	_ = post("/remote/reveal/review", review, c, false)
	h.mu.Lock()
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	next := rec
	next.Revision++
	next.Host = "replacement.example"
	if err := reg.Update(rec, next, targetregistry.ReviewedUpdate{Previous: rec, Next: next}); err != nil {
		t.Fatal(err)
	}
	approve.Set("token", token)
	if out := post("/remote/reveal/approve", approve, c, false); out.Code != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatal("stale record accepted", out.Code)
	}
	// Expired confirmations are consumed without invoking the executor.
	_ = post("/remote/reveal/review", review, c, false)
	h.mu.Lock()
	for k, v := range h.approvals {
		token = k
		v.expires = time.Now().Add(-time.Second)
		h.approvals[k] = v
	}
	h.mu.Unlock()
	approve.Set("token", token)
	if out := post("/remote/reveal/approve", approve, c, false); out.Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatal("expired confirmation accepted")
	}
	// A failed durable outcome never delivers values or raw backend errors.
	audit.failBegin.Store(true)
	_ = post("/remote/reveal/review", review, c, false)
	h.mu.Lock()
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	approve.Set("token", token)
	before := calls.Load()
	outBefore := post("/remote/reveal/approve", approve, c, false)
	if outBefore.Code != http.StatusServiceUnavailable || calls.Load() != before || strings.Contains(outBefore.Body.String(), "sensitive audit error") {
		t.Fatal("failed intent did not deny")
	}
	audit.failBegin.Store(false)
	audit.failFinish.Store(true)
	_ = post("/remote/reveal/review", review, c, false)
	h.mu.Lock()
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	approve.Set("token", token)
	out := post("/remote/reveal/approve", approve, c, false)
	if out.Code != http.StatusServiceUnavailable || strings.Contains(out.Body.String(), "secret-value") || strings.Contains(out.Body.String(), "sensitive audit error") {
		t.Fatal("failed audit leaked", out.Code)
	}
	audit.failFinish.Store(false)
	// A second confirmation is interrupted by real logout while the agent call is blocked.
	block = true
	entered = make(chan struct{})
	_ = post("/remote/reveal/review", review, c, false)
	h.mu.Lock()
	token = ""
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	approve.Set("token", token)
	done := make(chan *deadlineRecorder, 1)
	go func() { done <- post("/remote/reveal/approve", approve, c, false) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not entered")
	}
	logout := httptest.NewRecorder()
	lr := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lr.AddCookie(c)
	a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(logout, lr)
	select {
	case w = <-done:
		if strings.Contains(w.Body.String(), "secret-value") {
			t.Fatal("delivered after logout")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout did not cancel")
	}
	// A blocked response write is interrupted by logout via the write deadline.
	c3, csrf3 := login()
	block = false
	entered = nil
	review.Set("_csrf", csrf3)
	_ = post("/remote/reveal/review", review, c3, false)
	h.mu.Lock()
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	form := url.Values{"_csrf": {csrf3}, "token": {token}, "confirm": {"yes"}}
	request := httptest.NewRequest("POST", "/remote/reveal/approve", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(c3)
	bw := &blockedResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), unblock: make(chan struct{})}
	finished := make(chan struct{})
	go func() { handler.ServeHTTP(bw, request); close(finished) }()
	select {
	case <-bw.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write not started")
	}
	logout3 := httptest.NewRecorder()
	lr3 := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf3}}.Encode()))
	lr3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lr3.AddCookie(c3)
	a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(logout3, lr3)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("logout failed to interrupt response write")
	}
	if strings.Contains(bw.Body.String(), "secret-value") || bw.Body.Len() != 0 {
		t.Fatal("partial or second response")
	}
}
