package remoteui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/auth"
)

const testID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const otherID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type fake struct {
	reveals, enrolls, inspects, logCalls int
	audits                               []string
	beginErr, finishErr                  error
	inspectID                            string
	inspection                           *Inspection
	inventory                            []Container
}

func (*fake) AcquireRead(ctx context.Context, _ string) (context.Context, func() error, error) {
	return ctx, func() error { return nil }, nil
}

func (*fake) Hosts(context.Context) ([]Host, error) {
	return []Host{{ID: "h", Name: "Host <one>", State: "ready"}}, nil
}
func (f *fake) Inventory(context.Context, string) ([]Container, error) {
	if f.inventory != nil {
		return f.inventory, nil
	}
	return []Container{{ID: testID, Stack: "app", Service: "web"}}, nil
}
func (f *fake) Inspect(context.Context, string, string) (Inspection, error) {
	f.inspects++
	if f.inspection != nil {
		return *f.inspection, nil
	}
	id := f.inspectID
	if id == "" {
		id = testID
	}
	return Inspection{ID: id, Name: "app", Stack: "app", Service: "web", EnvKeys: []string{"TOKEN"}, Labels: map[string]string{"dill.managed": "true", "dill.stack": "app", "dill.service": "web"}}, nil
}

type fakeStream struct {
	events []LogEvent
	err    error
}

func (s *fakeStream) Next(context.Context) (LogEvent, error) {
	if len(s.events) == 0 {
		if s.err != nil {
			return LogEvent{}, s.err
		}
		return LogEvent{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*fakeStream) Close() error { return nil }
func (f *fake) Logs(context.Context, string, string, int, bool) (LogStream, error) {
	f.logCalls++
	return &fakeStream{events: []LogEvent{{Type: "chunk", Stream: "stdout", Bytes: []byte("log")}, {Type: "end", Reason: "complete"}}}, nil
}
func (f *fake) Reveal(context.Context, string, string, Confirmation) ([]Env, error) {
	f.reveals++
	return []Env{{"TOKEN", "secret-value"}}, nil
}
func (f *fake) Audit(_ context.Context, op string, _ bool) { f.audits = append(f.audits, op) }
func (f *fake) BeginRevealAudit(context.Context, RevealIntent) (string, error) {
	f.audits = append(f.audits, "intent")
	return "audit", f.beginErr
}
func (f *fake) FinishRevealAudit(context.Context, string, bool) error {
	f.audits = append(f.audits, "outcome")
	return f.finishErr
}
func setup(t *testing.T) (*auth.Authenticator, *fake, http.Handler, *http.Cookie, string) {
	t.Helper()
	a, e := auth.New(auth.Config{User: "admin", Password: "secret", APIToken: "token"})
	if e != nil {
		t.Fatal(e)
	}
	f := &fake{}
	h, e := New(f, a)
	if e != nil {
		t.Fatal(e)
	}
	login := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, req)
	cookie := login.Result().Cookies()[0]
	handler := h.Protected()
	get := httptest.NewRequest("GET", "/remote/inspect?host=h&container="+testID, nil)
	get.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	m := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if len(m) != 2 {
		t.Fatalf("missing CSRF: %s", rec.Body.String())
	}
	f.inspects = 0 // discard the setup inspection used to obtain the CSRF token
	return a, f, handler, cookie, m[1]
}
func TestRevealAuthorizationAndNoStore(t *testing.T) {
	_, f, h, c, csrf := setup(t)
	post := func(cookie *http.Cookie, token, confirmation string) *httptest.ResponseRecorder {
		v := url.Values{"host": {"h"}, "container": {testID}, "confirm": {confirmation}, "_csrf": {csrf}}
		r := httptest.NewRequest("POST", "/remote/reveal", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(nil, "token", "yes"); w.Code != 403 || f.reveals != 0 {
		t.Fatalf("bearer reveal: %d", w.Code)
	}
	if w := post(c, "", "no"); w.Code != 400 || f.reveals != 0 {
		t.Fatalf("unconfirmed: %d", w.Code)
	}
	w := post(c, "", "yes")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "secret-value") || f.reveals != 1 {
		t.Fatalf("reveal: %d %s", w.Code, w.Body.String())
	}
	if len(f.audits) != 2 || f.audits[0] != "intent" || f.audits[1] != "outcome" {
		t.Fatal(f.audits)
	}
	noauth, _ := auth.New(auth.Config{NoAuth: true})
	if _, err := New(f, noauth); err == nil || f.reveals != 1 {
		t.Fatal("no-auth configuration accepted")
	}
}
func TestBrowserFixture(t *testing.T) {
	path := os.Getenv("BRINE_REMOTE_FIXTURE")
	if path == "" {
		t.Skip("browser fixture only")
	}
	_, f, h, c, _ := setup(t)
	f.inspection = inspectionFixture()
	fixturePath := "/remote/inspect?host=h&container=" + testID
	if os.Getenv("BRINE_REMOTE_INVENTORY") != "" {
		f.inventory = []Container{{ID: testID, Name: "same", Stack: "app", Service: "web"}, {ID: otherID, Name: "same", Stack: "app", Service: "web"}}
		fixturePath = "/remote/inventory?host=h"
	}
	r := httptest.NewRequest("GET", fixturePath, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := os.WriteFile(path, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestInventoryDuplicateNamesHaveAccessibleIDs(t *testing.T) {
	_, f, h, c, _ := setup(t)
	f.inventory = []Container{{ID: testID, Name: "same", Stack: "app", Service: "web"}, {ID: otherID, Name: "same", Stack: "app", Service: "web"}}
	r := httptest.NewRequest("GET", "/remote/inventory?host=h", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, id := range []string{testID, otherID} {
		if !strings.Contains(body, `<span class="sr-only">Full container ID: `+id+`</span>`) || !strings.Contains(body, ">"+id[:12]+"</code>") || !strings.Contains(body, "container="+id) {
			t.Fatalf("missing accessible ID for %s: %s", id, body)
		}
	}
}
func TestViewsAndCSRF(t *testing.T) {
	_, f, h, c, _ := setup(t)
	for _, path := range []string{"/remote", "/remote/inventory?host=h", "/remote/inspect?host=h&container=" + testID} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "secret-value") {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/remote/enroll", strings.NewReader("host=h&confirm=yes"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || f.enrolls != 0 {
		t.Fatalf("CSRF: %d", w.Code)
	}
}

func TestRevokedAfterMiddlewareCannotReveal(t *testing.T) {
	a, f, _, cookie, csrf := setup(t)
	h, _ := New(f, a)
	wrapped := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logout := httptest.NewRequest("POST", "/logout", nil)
		logout.AddCookie(cookie)
		a.HandleLogout(httptest.NewRecorder(), logout)
		h.serve(w, r)
	}))
	form := url.Values{"host": {"h"}, "container": {testID}, "confirm": {"yes"}, "_csrf": {csrf}}
	req := httptest.NewRequest("POST", "/remote/reveal", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || f.reveals != 0 {
		t.Fatalf("revoked session: %d, calls %d", w.Code, f.reveals)
	}
}
func TestLogTerminalStates(t *testing.T) {
	tests := []struct {
		name   string
		events []LogEvent
		err    error
		want   string
	}{
		{"complete", []LogEvent{{Type: "chunk", Stream: "stdout", Bytes: []byte("hello")}, {Type: "end", Reason: "complete"}}, nil, `"reason":"complete"`},
		{"agent byte limit", []LogEvent{{Type: "end", Reason: "byte_limit"}}, nil, `"reason":"byte_limit"`},
		{"missing end", nil, io.EOF, `"reason":"incomplete"`},
		{"invalid chunk", []LogEvent{{Type: "chunk", Stream: "stdout", Bytes: make([]byte, 16385)}}, nil, `"reason":"invalid_stream"`},
		{"backend error", nil, errors.New("secret backend details"), `"reason":"unavailable"`},
	}
	chunks := make([]LogEvent, 65)
	for i := range chunks {
		chunks[i] = LogEvent{Type: "chunk", Stream: "stdout", Bytes: make([]byte, 16384)}
	}
	tests = append(tests, struct {
		name   string
		events []LogEvent
		err    error
		want   string
	}{"local cap", chunks, nil, `"reason":"truncated"`})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeLogStream(context.Background(), w, &fakeStream{events: tt.events, err: tt.err})
			if !strings.Contains(w.Body.String(), tt.want) || strings.Contains(w.Body.String(), "secret backend details") {
				t.Fatalf("unexpected stream ending")
			}
		})
	}
}
func TestSessionStatus(t *testing.T) {
	_, _, h, c, _ := setup(t)
	r := httptest.NewRequest("GET", "/remote/session", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "remainingMs") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/remote/session", nil)
	r.Header.Set("Authorization", "Bearer token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("bearer session accepted")
	}
}
