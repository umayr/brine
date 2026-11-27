package remoteui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestBrokerGuardDeniesLegacyRoutesButAllowsRecovery(t *testing.T) {
	h := &Deployment{pending: map[string]deploymentReview{"review": {Session: "session", Stack: "actual", Expiry: time.Now().Add(time.Minute)}}}
	var guarded string
	h.SetLegacyGuard(func(_ context.Context, stack string, fn func()) error {
		guarded = stack
		return errors.New("protected")
	})
	for _, path := range []string{"/create", "/review", "/stage", "/remove-preview", "/git-config"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", deployPath+path, nil)
		r.PostForm = url.Values{"stack": {"forged"}, "mode": {"enable"}, "id": {"review"}}
		called := false
		if !h.guardOrdinary(w, r, context.Background(), "session", func(http.ResponseWriter) { called = true }) || called || w.Code != 409 {
			t.Fatal("ordinary route not denied", path)
		}
		want := "forged"
		if path == "/create" {
			want = "actual"
		}
		if guarded != want {
			t.Fatal("wrong stack authorized")
		}
	}
	for _, path := range []string{"/recover", "/status", "/git", "/git-config"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", deployPath+path, nil)
		r.PostForm = url.Values{"mode": {"disable"}}
		if h.guardOrdinary(w, r, context.Background(), "session", func(http.ResponseWriter) {}) {
			t.Fatal("recovery/settings denial", path)
		}
	}
}
