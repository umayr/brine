package remoteui

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/targetregistry"
)

type enrollmentObserverFixture struct {
	calls         int
	changed       bool
	waitForCancel bool
	entered       chan struct{}
}

func (o *enrollmentObserverFixture) Observe(ctx context.Context, r targetregistry.Record) (targetregistry.Record, error) {
	o.calls++
	if o.entered != nil {
		close(o.entered)
	}
	if o.waitForCancel {
		<-ctx.Done()
		return targetregistry.Record{}, ctx.Err()
	}
	if o.changed {
		r.Host = "other.example"
	}
	return r, nil
}

func TestEnrollmentHTTPExactReviewAndFreshObservation(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", APIToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "targets"))
	if err != nil {
		t.Fatal(err)
	}
	obs := &enrollmentObserverFixture{}
	h, err := NewEnrollmentHandler(a, reg, obs)
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Protected()
	login := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, req)
	cookie := login.Result().Cookies()[0]
	get := httptest.NewRequest("GET", "/remote/enrollment", nil)
	get.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(&enrollmentTestWriter{w}, get)
	csrfMatch := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(csrfMatch) != 2 {
		t.Fatal(w.Body.String())
	}
	csrf := csrfMatch[1]
	post := func(path string, values url.Values, c *http.Cookie, bearer bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c != nil {
			r.AddCookie(c)
		}
		if bearer {
			r.Header.Set("Authorization", "Bearer token")
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(&enrollmentTestWriter{out}, r)
		return out
	}
	rec := enrollmentRecord(t)
	values := url.Values{"_csrf": {csrf}}
	for _, f := range enrollmentFields(rec) {
		values.Set(f.Name, f.Value)
	}
	if out := post("/remote/enrollment/review", values, nil, true); out.Code == 200 || obs.calls != 0 {
		t.Fatal("bearer accepted")
	}
	invalid := url.Values{}
	for k, v := range values {
		invalid[k] = v
	}
	invalid.Set("_csrf", "bad")
	if out := post("/remote/enrollment/review", invalid, cookie, false); out.Code == 200 || obs.calls != 0 {
		t.Fatal("invalid csrf accepted")
	}
	out := post("/remote/enrollment/review", values, cookie, false)
	body := out.Body.String()
	if out.Code != 200 || obs.calls != 1 {
		t.Fatal(out.Code, body)
	}
	for _, f := range displayEnrollmentFields(rec) {
		if !strings.Contains(html.UnescapeString(body), f.Value) {
			t.Fatalf("missing field %s", f.Name)
		}
	}
	m := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal(body)
	}
	approve := url.Values{"_csrf": {csrf}, "token": {m[1]}, "confirm": {"yes"}}
	entry := h.approvals.items[m[1]]
	type approvalControl struct {
		cancel   context.CancelFunc
		deadline time.Time
	}
	controls := make(chan approvalControl, 1)
	h.approvalContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithDeadline(parent, deadline)
		controls <- approvalControl{cancel, deadline}
		return ctx, cancel
	}
	obs.waitForCancel = true
	obs.entered = make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- post("/remote/enrollment/approve", approve, cookie, false) }()
	select {
	case <-obs.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("approval never entered observation")
	}
	h.approvals.mu.Lock()
	_, stillPresent := h.approvals.items[m[1]]
	h.approvals.mu.Unlock()
	if stillPresent {
		t.Fatal("token not consumed before observation")
	}
	control := <-controls
	if !control.deadline.Equal(entry.expires) {
		t.Fatal("approval deadline not retained")
	}
	control.cancel()
	out = <-result
	h.approvalContext = nil
	obs.entered = nil
	if _, err := reg.Get(rec.ID); err == nil || strings.Contains(out.Body.String(), "Enrollment approved") {
		t.Fatal("expired observation enrolled")
	}
	obs.waitForCancel = false
	out = post("/remote/enrollment/review", values, cookie, false)
	m = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if len(m) != 2 {
		t.Fatal(out.Body.String())
	}
	approve.Set("token", m[1])
	callsBefore := obs.calls
	obs.changed = true
	out = post("/remote/enrollment/approve", approve, cookie, false)
	if obs.calls != callsBefore+1 || strings.Contains(out.Body.String(), "Enrollment approved") {
		t.Fatal("changed observation accepted")
	}
	if _, err := reg.Get(rec.ID); err == nil {
		t.Fatal("record added")
	}
	obs.changed = false
	out = post("/remote/enrollment/approve", approve, cookie, false)
	if out.Code != http.StatusForbidden {
		t.Fatal("replay accepted", out.Code)
	}
	out = post("/remote/enrollment/review", values, cookie, false)
	m = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if len(m) != 2 {
		t.Fatal(out.Body.String())
	}
	approve.Set("token", m[1])
	out = post("/remote/enrollment/approve", approve, cookie, false)
	got, err := reg.Get(rec.ID)
	if err != nil || got != rec || !strings.Contains(out.Body.String(), "Enrollment approved") {
		t.Fatal("enrollment failed", err, out.Body.String())
	}

	// Update review displays both complete snapshots. Another authenticated
	// session cannot consume the token even with its own valid CSRF token.
	next := rec
	next.Revision++
	next.Host = "next.example"
	for _, field := range enrollmentFields(next) {
		values.Set(field.Name, field.Value)
	}
	out = post("/remote/enrollment/review", values, cookie, false)
	if !strings.Contains(out.Body.String(), "Previous record") || !strings.Contains(out.Body.String(), rec.Host) || !strings.Contains(out.Body.String(), next.Host) {
		t.Fatal("old/new records not displayed")
	}
	m = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if len(m) != 2 {
		t.Fatal("missing update token")
	}
	approve.Set("token", m[1])
	_, held, err := reg.AcquireRead(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry = h.approvals.items[m[1]]
	h.approvalContext = func(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithDeadline(parent, deadline)
		controls <- approvalControl{cancel, deadline}
		return ctx, cancel
	}
	commitEntered := make(chan struct{})
	h.beforeCommit = func() { close(commitEntered) }
	go func() { result <- post("/remote/enrollment/approve", approve, cookie, false) }()
	select {
	case <-commitEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("commit did not start")
	}
	h.approvals.mu.Lock()
	_, stillPresent = h.approvals.items[m[1]]
	h.approvals.mu.Unlock()
	if stillPresent {
		t.Fatal("token not consumed before locked commit")
	}
	control = <-controls
	if !control.deadline.Equal(entry.expires) {
		t.Fatal("commit deadline not retained")
	}
	control.cancel()
	out = <-result
	h.approvalContext = nil
	h.beforeCommit = nil
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = reg.Get(rec.ID)
	if err != nil || got != rec || !strings.Contains(out.Body.String(), "outcome uncertain") {
		t.Fatal("expired locked commit published", got, err, out.Body.String())
	}
	out = post("/remote/enrollment/review", values, cookie, false)
	m = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(out.Body.String())
	if len(m) != 2 {
		t.Fatal("missing second update token")
	}
	approve.Set("token", m[1])
	login2 := httptest.NewRecorder()
	request2 := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	request2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login2, request2)
	other := login2.Result().Cookies()[0]
	otherGet := httptest.NewRequest("GET", "/remote/enrollment", nil)
	otherGet.AddCookie(other)
	otherPage := httptest.NewRecorder()
	handler.ServeHTTP(&enrollmentTestWriter{otherPage}, otherGet)
	otherCSRF := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(otherPage.Body.String())
	if len(otherCSRF) != 2 {
		t.Fatal("missing second csrf")
	}
	otherApproval := url.Values{"_csrf": {otherCSRF[1]}, "token": {m[1]}, "confirm": {"yes"}}
	if out = post("/remote/enrollment/approve", otherApproval, other, false); out.Code != http.StatusForbidden {
		t.Fatal("other session accepted", out.Code)
	}
	concurrent := rec
	concurrent.Revision++
	concurrent.Host = "concurrent.example"
	if err = reg.Update(rec, concurrent, targetregistry.ReviewedUpdate{Previous: rec, Next: concurrent}); err != nil {
		t.Fatal(err)
	}
	out = post("/remote/enrollment/approve", approve, cookie, false)
	got, err = reg.Get(rec.ID)
	if err != nil || got != concurrent || strings.Contains(out.Body.String(), "Enrollment approved") {
		t.Fatal("stale update accepted", got, err)
	}
}

// enrollmentTestWriter models deadline-capable net/http responses in unit tests.
type enrollmentTestWriter struct{ *httptest.ResponseRecorder }

func (*enrollmentTestWriter) SetWriteDeadline(time.Time) error { return nil }
func (*enrollmentTestWriter) SetReadDeadline(time.Time) error  { return nil }
