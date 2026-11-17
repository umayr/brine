package remoteui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/targetregistry"
)

// A real loopback transport is wrapped at the write boundary to deterministically
// model a peer that stops reading. This exercises both HTTP protocol stacks.
func TestEnrollmentBlockedDeliveryBoundsLogout(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		for _, phase := range []string{"write", "flush"} {
			t.Run(map[bool]string{false: "http1", true: "http2"}[h2]+"/"+phase, func(t *testing.T) {
				a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
				if err != nil {
					t.Fatal(err)
				}
				reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
				if err != nil {
					t.Fatal(err)
				}
				h, err := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
				if err != nil {
					t.Fatal(err)
				}
				h.requestDuration = func() time.Duration { return 150 * time.Millisecond }
				login := httptest.NewRecorder()
				lr := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
				lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				a.HandleLogin(login, lr)
				cookie := login.Result().Cookies()[0]
				cw := httptest.NewRecorder()
				cr := httptest.NewRequest("GET", "/csrf", nil)
				cr.AddCookie(cookie)
				a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(a.CSRFFor(r))) })).ServeHTTP(cw, cr)
				csrf := cw.Body.String()
				form := url.Values{"_csrf": {csrf}}
				for _, f := range enrollmentFields(enrollmentRecord(t)) {
					form.Set(f.Name, f.Value)
				}
				entered, expired, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(finished)
					h.Protected().ServeHTTP(&stalledWriter{ResponseWriter: w, phase: phase, entered: entered, expired: expired}, r)
				}))
				ts.EnableHTTP2 = h2
				if h2 {
					ts.StartTLS()
				} else {
					ts.Start()
				}
				defer ts.Close()
				done := make(chan struct{})
				go func() {
					defer close(done)
					req, _ := http.NewRequest("POST", ts.URL+"/remote/enrollment/review", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.AddCookie(cookie)
					resp, e := ts.Client().Do(req)
					if e == nil {
						resp.Body.Close()
					}
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("flush not reached")
				}
				loggedOut := make(chan struct{})
				go func() {
					defer close(loggedOut)
					w := httptest.NewRecorder()
					r := httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{"_csrf": {csrf}}.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					a.Middleware(http.HandlerFunc(a.HandleLogout)).ServeHTTP(w, r)
				}()
				select {
				case <-loggedOut:
					t.Fatal("logout passed a live delivery lease")
				case <-time.After(20 * time.Millisecond):
				}
				select {
				case <-loggedOut:
				case <-time.After(2 * time.Second):
					t.Fatal("logout blocked past deadline")
				}
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("handler blocked")
				}
				<-done
			})
		}
	}
}

func TestEnrollmentRejectsUnsupportedDeadlines(t *testing.T) {
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
	if err != nil {
		t.Fatal(err)
	}
	observer := &enrollmentObserverFixture{}
	h, _ := NewEnrollmentHandler(a, reg, observer)
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("expected abort when deadlines unsupported")
		}
		if observer.calls != 0 {
			t.Fatal("operation reached observer")
		}
	}()
	h.Protected().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/remote/enrollment/review", nil))
}

func TestEnrollmentHTTPCompletionAndReuse(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[h2], func(t *testing.T) {
			a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
			reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "registry"))
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewEnrollmentHandler(a, reg, &enrollmentObserverFixture{})
			if err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRecorder()
			lr := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
			lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			a.HandleLogin(login, lr)
			cookie := login.Result().Cookies()[0]
			ts := httptest.NewUnstartedServer(h.Protected())
			ts.EnableHTTP2 = h2
			if h2 {
				ts.StartTLS()
			} else {
				ts.Start()
			}
			defer ts.Close()
			client := ts.Client()
			for i := 0; i < 2; i++ {
				reused := false
				req, _ := http.NewRequest("GET", ts.URL+"/remote/enrollment", nil)
				req.AddCookie(cookie)
				req = req.WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), "</html>") {
					t.Fatalf("incomplete response: %v status %d", err, resp.StatusCode)
				}
				if h2 && resp.ProtoMajor != 2 {
					t.Fatalf("expected HTTP/2, got %s", resp.Proto)
				}
				if i == 1 && !reused {
					t.Fatal("connection not reused")
				}
			}
		})
	}
}
