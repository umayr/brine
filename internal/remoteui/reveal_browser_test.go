package remoteui

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/targetregistry"
)

// TestRevealBrowserFixture records the actual protected review response. The
// browser replaces approval/session endpoints with controlled synthetic data.
func TestRevealBrowserFixture(t *testing.T) {
	path := os.Getenv("BRINE_REVEAL_FIXTURE")
	if path == "" {
		t.Skip("browser fixture only")
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	rec.DaemonID = "daemon  x"
	if err = reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	e := &remotereveal.Executor{Registry: reg, Audit: &reviewAudit{}, Signer: reviewSigner{ed25519.NewKeyFromSeed(make([]byte, 32))}, Open: func(_ context.Context, _ targetregistry.Record, _ ed25519.PublicKey) (remotereveal.Session, error) {
		return nil, nil
	}}
	h, err := NewRevealHandler(a, reg, e)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, r)
	cookie := login.Result().Cookies()[0]
	csrf := httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/csrf", nil)
	r.AddCookie(cookie)
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })).ServeHTTP(csrf, r)
	form := url.Values{"_csrf": {csrf.Body.String()}, "host": {rec.ID}, "container": {strings.Repeat("a", 64)}}
	r = httptest.NewRequest("POST", "/remote/reveal/review", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.Protected().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	r2 := httptest.NewRequest("POST", "/remote/reveal/review", strings.NewReader(form.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	h.Protected().ServeHTTP(w2, r2)
	if w2.Code != 200 || w.Header().Get("Content-Security-Policy") == w2.Header().Get("Content-Security-Policy") {
		t.Fatal("CSP nonce was not fresh")
	}
	if strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("cookie present in review HTML")
	}
	data, err := json.Marshal(map[string]string{"html": w.Body.String(), "csp": w.Header().Get("Content-Security-Policy"), "frame": w.Header().Get("X-Frame-Options")})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
