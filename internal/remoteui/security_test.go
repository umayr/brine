package remoteui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestEnrollmentReviewBoundAndOneShot(t *testing.T) {
	a, f, h, c, csrf := setup(t)
	reviewToken := func() string {
		r := httptest.NewRequest("GET", "/remote/review?host=host-A", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		m := regexp.MustCompile(`name="reviewToken" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(m) != 2 {
			t.Fatalf("review failed %s", w.Body.String())
		}
		return m[1]
	}
	post := func(cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/remote/enroll", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	token := reviewToken()
	form := url.Values{"_csrf": {csrf}, "confirm": {"yes"}, "reviewToken": {token}, "host": {"host-B"}}
	if w := post(c, form); w.Code != 400 || f.enrolls != 0 {
		t.Fatalf("substitution: %d", w.Code)
	}
	token = reviewToken()
	form = url.Values{"_csrf": {csrf}, "confirm": {"yes"}, "reviewToken": {token}}
	// Same authenticator, different cookie and CSRF session.
	l := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(l, req)
	other := l.Result().Cookies()[0]
	get := httptest.NewRequest("GET", "/remote/review?host=other", nil)
	get.AddCookie(other)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(gw.Body.String())
	if len(m) != 2 {
		t.Fatal("other CSRF")
	}
	otherForm := url.Values{"_csrf": {m[1]}, "confirm": {"yes"}, "reviewToken": {token}}
	if w := post(other, otherForm); w.Code != 403 || f.enrolls != 0 {
		t.Fatalf("other session: %d", w.Code)
	}
	if w := post(c, form); w.Code != 200 || f.enrolls != 1 || f.enrolledHost != "host-A" || f.enrolledIdentity.DaemonID != "daemon" {
		t.Fatalf("enroll: %d", w.Code)
	}
	if w := post(c, form); w.Code != 403 || f.enrolls != 1 {
		t.Fatalf("replay: %d", w.Code)
	}
	if safeText("safe\u202eevil") || safeText("bad\nname") {
		t.Fatal("control accepted")
	}
}
func TestRevealAuditFailClosed(t *testing.T) {
	_, f, h, c, csrf := setup(t)
	post := func() *httptest.ResponseRecorder {
		v := url.Values{"_csrf": {csrf}, "host": {"h"}, "container": {testID}, "confirm": {"yes"}}
		r := httptest.NewRequest("POST", "/remote/reveal", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	f.beginErr = errors.New("audit failed")
	if w := post(); w.Code != 503 || f.reveals != 0 || strings.Contains(w.Body.String(), "audit failed") {
		t.Fatal("intent failure did not deny")
	}
	f.beginErr = nil
	f.finishErr = errors.New("audit failed")
	if w := post(); w.Code != 503 || f.reveals != 1 || strings.Contains(w.Body.String(), "secret-value") {
		t.Fatal("outcome failure exposed values")
	}
}
func TestPreAuthBodyLimit(t *testing.T) {
	_, _, h, c, _ := setup(t)
	reader := &countReader{r: strings.NewReader("_csrf=" + strings.Repeat("a", 1<<20))}
	r := httptest.NewRequest("POST", "/remote/reveal", reader)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 || reader.n > 32769 {
		t.Fatalf("code %d bytes %d", w.Code, reader.n)
	}
}

type countReader struct {
	r io.Reader
	n int
}

func (r *countReader) Read(p []byte) (int, error) { n, e := r.r.Read(p); r.n += n; return n, e }
func TestCanonicalContainerIDRequiredBeforeBackend(t *testing.T) {
	_, f, h, c, csrf := setup(t)
	for _, id := range []string{"abc", strings.ToUpper(testID), strings.Repeat("g", 64), testID + "0"} {
		r := httptest.NewRequest("GET", "/remote/inspect?host=h&container="+id, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || strings.Contains(w.Body.String(), "Reveal values") {
			t.Fatalf("inspect accepted %q", id)
		}
		for _, path := range []string{"/remote/reveal", "/remote/logs"} {
			v := url.Values{"_csrf": {csrf}, "host": {"h"}, "container": {id}, "confirm": {"yes"}}
			r = httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(c)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("%s accepted %q: %d", path, id, w.Code)
			}
		}
	}
	if f.inspects != 0 || f.reveals != 0 || f.logCalls != 0 || len(f.audits) != 0 {
		t.Fatalf("backend invoked for invalid ID: %+v", f)
	}
}
func TestInspectIdentityAndUnsafeReview(t *testing.T) {
	_, f, h, c, _ := setup(t)
	f.inspectID = otherID
	r := httptest.NewRequest("GET", "/remote/inspect?host=h&container="+testID, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "identity mismatch") || strings.Contains(w.Body.String(), "Reveal values") {
		t.Fatal("mismatched inspect shown")
	}
	id, _ := f.Review(context.Background(), "h")
	id.DaemonID = "evil\u202eidentity"
	f.reviewOverride = &id
	r = httptest.NewRequest("GET", "/remote/review?host=h", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), "reviewToken") {
		t.Fatal("unsafe identity reviewed")
	}
}
func TestExpiredReviewAndEventCap(t *testing.T) {
	a, f, _, cookie, _ := setup(t)
	h, err := New(f, a)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/remote/review", nil)
	r.AddCookie(cookie)
	id, _ := f.Review(context.Background(), "h")
	token, err := h.saveReview(r, "h", id)
	if err != nil {
		t.Fatal(err)
	}
	entry := h.reviews[token]
	entry.expires = time.Now().Add(-time.Second)
	h.reviews[token] = entry
	if _, ok := h.takeReview(r, token); ok {
		t.Fatal("expired review accepted")
	}
	events := make([]LogEvent, 4100)
	for i := range events {
		events[i] = LogEvent{Type: "chunk", Stream: "stdout", Bytes: []byte("x")}
	}
	now := time.Now()
	clock := func() time.Time { now = now.Add(6 * time.Millisecond); return now }
	w := httptest.NewRecorder()
	writeLogStreamClock(context.Background(), w, &fakeStream{events: events}, clock)
	if !strings.Contains(w.Body.String(), `"reason":"truncated"`) {
		t.Fatal("event cap not enforced")
	}
}
func TestHeartbeatAndRateLimit(t *testing.T) {
	events := []LogEvent{{Type: "heartbeat"}, {Type: "heartbeat"}}
	w := httptest.NewRecorder()
	writeLogStream(context.Background(), w, &fakeStream{events: events})
	if !strings.Contains(w.Body.String(), "invalid_stream") {
		t.Fatal("rapid heartbeats accepted")
	}
	events = make([]LogEvent, 400)
	for i := range events {
		events[i] = LogEvent{Type: "chunk", Stream: "stdout", Bytes: []byte("x")}
	}
	w = httptest.NewRecorder()
	start := time.Now()
	writeLogStream(context.Background(), w, &fakeStream{events: events})
	if time.Since(start) < time.Second && !strings.Contains(w.Body.String(), "truncated") {
		t.Fatal("burst accepted")
	}
}
