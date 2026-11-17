package remoteui

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestActualSessionExpiryCancelsInFlightReveal(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", SessionTTL: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	hash := sha256.Sum256(key.Public().(ed25519.PublicKey))
	rec.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	if err = reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var grant agentclient.Grant
	entered := make(chan struct{})
	audit := &reviewAudit{}
	e := &remotereveal.Executor{Registry: reg, Audit: audit, Signer: reviewSigner{key}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (remotereveal.Session, error) {
		return &reviewSession{calls: &calls, entered: entered, wait: true, grant: &grant}, nil
	}}
	h, err := NewRevealHandler(a, reg, e)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, r)
	c := login.Result().Cookies()[0]
	csrf := httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/csrf", nil)
	r.AddCookie(c)
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })).ServeHTTP(csrf, r)
	post := func(path string, v url.Values) *deadlineRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(c)
		w := &deadlineRecorder{httptest.NewRecorder()}
		h.Protected().ServeHTTP(w, r)
		return w
	}
	post("/remote/reveal/review", url.Values{"_csrf": {csrf.Body.String()}, "host": {rec.ID}, "container": {strings.Repeat("a", 64)}})
	h.mu.Lock()
	token := ""
	for k := range h.approvals {
		token = k
	}
	h.mu.Unlock()
	if token == "" {
		t.Fatal("missing approval")
	}
	done := make(chan *deadlineRecorder, 1)
	go func() {
		done <- post("/remote/reveal/approve", url.Values{"_csrf": {csrf.Body.String()}, "token": {token}, "confirm": {"yes"}})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not entered")
	}
	select {
	case w := <-done:
		if strings.Contains(w.Body.String(), "secret-value") {
			t.Fatal("delivered after expiry")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("expiry did not cancel")
	}
}
