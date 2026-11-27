package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
)

func TestBrokerSessionRejectsExpiredRetainedRequest(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "test-only-password", SessionTTL: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("user=admin&password=test-only-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.HandleLogin(w, login)
	if w.Code != 303 || len(w.Result().Cookies()) == 0 {
		t.Fatal("login failed")
	}
	h := &Handler{auth: a}
	r := httptest.NewRequest(http.MethodGet, "/stacks/example", nil)
	r.AddCookie(w.Result().Cookies()[0])
	var retained *http.Request
	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		retained = r
		if h.brokerSession(r) == "" {
			t.Fatal("valid session refused")
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
	if retained == nil {
		t.Fatal("not authenticated")
	}
	time.Sleep(250 * time.Millisecond)
	// Middleware's original cookie/CSRF metadata is still present, but is not
	// current authorization for a slow preparation or final target callback.
	if a.CSRFFor(retained) == "" {
		t.Fatal("fixture lost request metadata")
	}
	if h.brokerSession(retained) != "" {
		t.Fatal("expired retained request authorized target work")
	}
}
