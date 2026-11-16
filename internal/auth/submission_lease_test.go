package auth

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSubmissionAndLogoutWaitsAreCancellable(t *testing.T) {
	a := newTestAuth(t, Config{})
	c := doLogin(t, a, "admin", "secret", "").Cookies()[0]
	sess := sessionFor(t, a, c)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	req = req.WithContext(withInfo(req.Context(), requestInfo{method: "cookie", user: sess.user, csrf: sess.csrf}))
	_, release, ok := a.SubmissionLease(req)
	if !ok {
		t.Fatal("first lease")
	}
	ctx, cancel := context.WithCancel(req.Context())
	result := make(chan bool, 1)
	go func() {
		_, release2, ok := a.SubmissionLease(req.WithContext(ctx))
		if ok {
			release2()
		}
		result <- ok
	}()
	cancel()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("cancelled lease acquired")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease wait stuck")
	}
	logoutCtx, stop := context.WithCancel(context.Background())
	logout := httptest.NewRequest("POST", "/logout", nil).WithContext(logoutCtx)
	logout.AddCookie(c)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); a.HandleLogout(w, logout); done <- w }()
	stop()
	select {
	case w := <-done:
		if w.Code != 408 {
			t.Fatalf("cancelled logout %d", w.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logout wait stuck")
	}
	release()
	if sessionFor(t, a, c) == nil {
		t.Fatal("cancelled logout revoked session")
	}
}
