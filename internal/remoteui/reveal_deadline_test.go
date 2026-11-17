package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/targetregistry"
)

type stalledWriter struct {
	http.ResponseWriter
	phase   string
	entered chan struct{}
	expired chan struct{}
	once    sync.Once
}

func (w *stalledWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *stalledWriter) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() {
		time.AfterFunc(time.Until(d), func() { w.once.Do(func() { close(w.expired) }) })
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(d)
}
func (w *stalledWriter) Write(p []byte) (int, error) {
	if w.phase == "write" {
		close(w.entered)
		<-w.expired
		return 0, errors.New("deadline")
	}
	return w.ResponseWriter.Write(p)
}
func (w *stalledWriter) FlushError() error {
	if w.phase == "flush" {
		close(w.entered)
		<-w.expired
		return errors.New("deadline")
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func TestRevealBoundedStalledResponse(t *testing.T) {
	for _, phase := range []string{"write", "flush", "release"} {
		t.Run(phase, func(t *testing.T) {
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
			var calls atomic.Int32
			var grant agentclient.Grant
			executor := &remotereveal.Executor{Registry: reg, Audit: &reviewAudit{}, Signer: reviewSigner{key}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (remotereveal.Session, error) {
				return &reviewSession{calls: &calls, grant: &grant}, nil
			}}
			h, err := NewRevealHandler(a, reg, executor)
			if err != nil {
				t.Fatal(err)
			}
			h.requestDuration = func() time.Duration { return 100 * time.Millisecond }
			login := httptest.NewRecorder()
			lr := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
			lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			a.HandleLogin(login, lr)
			cookie := login.Result().Cookies()[0]
			csrfW := httptest.NewRecorder()
			cr := httptest.NewRequest("GET", "/csrf", nil)
			cr.AddCookie(cookie)
			a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })).ServeHTTP(csrfW, cr)
			csrf := csrfW.Body.String()
			requestID, err := agentclient.NewRequestID()
			if err != nil {
				t.Fatal(err)
			}
			token, err := h.issue(cookie.Value, revealApproval{record: rec, container: strings.Repeat("a", 64), request: requestID})
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			expired := make(chan struct{})
			proceed := make(chan struct{})
			if phase == "release" {
				h.requestDuration = func() time.Duration { return 3 * time.Second }
				h.beforeRelease = func() { close(entered); <-proceed }
			}
			finished := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				h.Protected().ServeHTTP(&stalledWriter{ResponseWriter: w, phase: phase, entered: entered, expired: expired}, r)
			}))
			defer ts.Close()
			form := url.Values{"_csrf": {csrf}, "token": {token}, "confirm": {"yes"}}
			if phase == "write" {
				form.Set("_csrf", "invalid")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				req, _ := http.NewRequest("POST", ts.URL+"/remote/reveal/approve", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(cookie)
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("write or flush did not block")
			}
			if phase == "release" {
				logout := httptest.NewRecorder()
				lr := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
				lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				lr.AddCookie(cookie)
				a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(logout, lr)
				select {
				case <-expired:
				case <-time.After(time.Second):
					t.Fatal("logout did not expire deadline during release")
				}
				close(proceed)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("deadline did not bound stalled response")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("client did not finish")
			}
			if phase == "write" && calls.Load() != 0 {
				t.Fatal("bad CSRF reached executor")
			}
			if (phase == "flush" || phase == "release") && calls.Load() != 1 {
				t.Fatal("reveal did not reach flush")
			}
		})
	}
}
