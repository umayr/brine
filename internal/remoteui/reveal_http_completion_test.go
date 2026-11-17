package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
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

type watchedHTTPWriter struct {
	http.ResponseWriter
	expired chan struct{}
	once    sync.Once
}

func (w *watchedHTTPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *watchedHTTPWriter) SetWriteDeadline(d time.Time) error {
	err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(d)
	if !d.IsZero() && !d.After(time.Now()) {
		w.once.Do(func() { close(w.expired) })
	}
	return err
}

// Use real HTTP/1.1 framing. Keep the outer handler alive after the inner
// handler returns so a wrongly triggered deadline watcher deterministically
// gets a chance to expire the socket before net/http writes the final chunk.
func TestRevealApprovalCompletesHTTPResponse(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[http2], func(t *testing.T) { testRevealApprovalCompletesHTTPResponse(t, http2) })
	}
}

func testRevealApprovalCompletesHTTPResponse(t *testing.T, http2 bool) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	hash := sha256.Sum256(key.Public().(ed25519.PublicKey))
	rec := enrollmentRecord(t)
	rec.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	if err = reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "audit")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	audit, err := revealaudit.Initialize(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var grant agentclient.Grant
	e := &remotereveal.Executor{Registry: reg, Audit: audit, Signer: reviewSigner{key}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (remotereveal.Session, error) {
		return &reviewSession{calls: &calls, grant: &grant}, nil
	}}
	h, err := NewRevealHandler(a, reg, e)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.HandleLogin)
	mux.Handle("/csrf", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })))
	mux.Handle("/remote/reveal/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &watchedHTTPWriter{ResponseWriter: w, expired: make(chan struct{})}
		h.Protected().ServeHTTP(wrapped, r)
		if r.URL.Path == "/remote/reveal/approve" {
			select {
			case <-wrapped.expired:
			case <-time.After(200 * time.Millisecond):
			}
		}
	}))
	ts := httptest.NewUnstartedServer(mux)
	if http2 {
		ts.EnableHTTP2 = true
		ts.StartTLS()
	} else {
		ts.Start()
	}
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 3 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var reused atomic.Bool
	do := func(method, path string, form url.Values, c *http.Cookie) (int, string, http.Header, error) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if path == "/remote/reveal/approve" {
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
		}
		if c != nil {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header, err
	}
	_, _, header, err := do("POST", "/login", url.Values{"user": {"admin"}, "password": {"secret"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookies := (&http.Response{Header: header}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing session")
	}
	cookie := cookies[0]
	_, csrf, _, err := do("GET", "/csrf", nil, cookie)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	code, _, _, err := do("POST", "/remote/reveal/review", url.Values{"_csrf": {csrf}, "host": {rec.ID}, "container": {id}}, cookie)
	if err != nil || code != 200 {
		t.Fatalf("review: %d %v", code, err)
	}
	h.mu.Lock()
	token := ""
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	if token == "" {
		t.Fatal("missing approval")
	}
	form := url.Values{"_csrf": {csrf}, "token": {token}, "confirm": {"yes"}}
	code, body, _, err := do("POST", "/remote/reveal/approve", form, cookie)
	if err != nil || code != 200 || !strings.Contains(body, "secret-value") {
		t.Fatalf("incomplete approval response: status=%d error=%v body=%q", code, err, body)
	}
	entries, err := audit.Entries(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Outcome != revealaudit.Succeeded {
		t.Fatalf("audit outcome: %v %#v", err, entries)
	}
	code, _, _, err = do("POST", "/remote/reveal/approve", form, cookie)
	if err != nil || code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("replay status=%d error=%v calls=%d", code, err, calls.Load())
	}
	if !reused.Load() {
		t.Fatal("connection was not reusable after approval")
	}
}
