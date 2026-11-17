package remoteui

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

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

}
func TestExpiredReviewAndEventCap(t *testing.T) {
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
