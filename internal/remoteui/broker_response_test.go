package remoteui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBrokerGuardDoesNotPublishReceiptBeforeFinalFence(t *testing.T) {
	h := &Deployment{}
	h.SetLegacyGuard(func(_ context.Context, _ string, fn func()) error {
		fn()
		return errors.New("post-dispatch fence unavailable")
	})
	r := httptest.NewRequest("POST", deployPath+"/stage", nil)
	r.PostForm = url.Values{"stack": {"demo"}}
	w := httptest.NewRecorder()
	if !h.guardOrdinary(w, r, context.Background(), "session", func(response http.ResponseWriter) { _, _ = response.Write([]byte("SYNTHETIC_TERMINAL_SUCCESS")) }) || w.Code != 503 || strings.Contains(w.Body.String(), "SYNTHETIC_TERMINAL_SUCCESS") || !strings.Contains(w.Body.String(), "do not replay") {
		t.Fatal("failed final fence published receipt", w.Code, w.Body.String())
	}
}
