package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

type rootOverrideStore struct {
	store.StackStore
	mu   sync.Mutex
	root string
}

func (s *rootOverrideStore) Get(name string) (*store.Stack, error) {
	stack, err := s.StackStore.Get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && s.root != "" {
		stack.PklPath = filepath.Join(s.root, filepath.Base(stack.PklPath))
	}
	return stack, err
}

func TestStagingCookieConfirmationAndExactBytes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "source&root")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base, e := store.NewFSStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	st := &rootOverrideStore{StackStore: base}
	stack, e := st.Create("app", "original")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(stack.PklPath), "a&b.txt"), []byte("data"), 0600); e != nil {
		t.Fatal(e)
	}
	vars, e := configvars.New(dir, st)
	if e != nil {
		t.Fatal(e)
	}
	if e = vars.Set("", "REGION", "north", false); e != nil {
		t.Fatal(e)
	}
	runner := dill.New("dill")
	runner.Variables = vars
	ag := agent.NewLocalAgent(nil, runner)
	// Short socket path for Darwin's sun_path limit.
	sockdir, e := os.MkdirTemp("", "brst-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(sockdir)
	sock := filepath.Join(sockdir, "s")
	l, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	var mu sync.Mutex
	var bodies [][]byte
	unknown := false
	var statusEntered, statusRelease chan struct{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
		case "/v1/status":
			mu.Lock()
			entered, release := statusEntered, statusRelease
			mu.Unlock()
			if entered != nil {
				close(entered)
				<-release
			}
			io.WriteString(w, `{"stack":"app","engine":"docker","containers":[]}`)
		case "/v1/snapshots":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, b)
			u := unknown
			mu.Unlock()
			if u {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"status":"staged","stage_id":"0123456789abcdef0123456789abcdef"}`)
		}
	})}
	go srv.Serve(l)
	defer srv.Close()
	a, e := auth.New(auth.Config{User: "admin", Password: "test-only-password", APIToken: "test-token"})
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.Default()
	cfg.StagingSocket = sock
	cfg.StagingIdentity = "lab"
	h := New(ag, st, nil, nil, "docker", false, a, 0)
	h.EnableLocalStaging(cfg)
	defer h.CloseStaging()
	r := chi.NewRouter()
	r.Use(a.Middleware)
	admitted := make(chan struct{}, 1)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Admitted") == "1" {
				admitted <- struct{}{}
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/csrf", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, a.CSRFFor(r)) })
	r.Post("/logout", a.HandleLogout)
	Mount(r, h, httpx.Timeouts{})
	login := func() (*http.Cookie, string) {
		f := url.Values{"user": {"admin"}, "password": {"test-only-password"}}
		q := httptest.NewRequest("POST", "/login", strings.NewReader(f.Encode()))
		q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.HandleLogin(w, q)
		c := w.Result().Cookies()[0]
		q = httptest.NewRequest("GET", "/csrf", nil)
		q.AddCookie(c)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return c, w.Body.String()
	}
	call := func(path string, c *http.Cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
		q := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
		q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c != nil {
			q.AddCookie(c)
		}
		if csrf != "" {
			q.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w
	}
	prep := "/web/stacks/app/staging/prepare"
	confirm := "/web/stacks/app/staging/confirm"
	c, csrf := login()
	c2, csrf2 := login()
	if w := call(prep, c, "", nil); w.Code != 403 {
		t.Fatalf("csrf: %d", w.Code)
	}
	// Bearer variable writes retain existing non-cookie authorization semantics.
	bearerForm := url.Values{"name": {"BEARER"}, "value": {"ok"}, "action": {"save"}}
	bearerReq := httptest.NewRequest("POST", "/variables", strings.NewReader(bearerForm.Encode()))
	bearerReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bearerReq.Header.Set("Authorization", "Bearer test-token")
	bearerResp := httptest.NewRecorder()
	r.ServeHTTP(bearerResp, bearerReq)
	if bearerResp.Code != 303 {
		t.Fatalf("bearer variable save %d", bearerResp.Code)
	}
	q := httptest.NewRequest("POST", prep, nil)
	q.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	if w.Code != 401 {
		t.Fatalf("bearer: %d", w.Code)
	}
	w = call(prep, c, csrf, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "BRINE_VAR_REGION") || !strings.Contains(w.Body.String(), "a&amp;b.txt") || !strings.Contains(w.Body.String(), "source&amp;root") || !strings.Contains(w.Body.String(), "local stack") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("prepare %d %s", w.Code, w.Body.String())
	}
	digest := sha256.Sum256([]byte("data"))
	if !strings.Contains(w.Body.String(), hex.EncodeToString(digest[:])) || !strings.Contains(w.Body.String(), "File contents are not displayed") {
		t.Fatal("captured file digest or content-review limitation absent")
	}
	re := regexp.MustCompile(`name="review" value="([a-f0-9]+)"`)
	m := re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal("review absent")
	}
	id := m[1]
	mu.Lock()
	n := len(bodies)
	mu.Unlock()
	if n != 0 {
		t.Fatal("upload before confirmation")
	}
	if w = call(confirm, c2, csrf2, url.Values{"review": {id}}); w.Code != 409 {
		t.Fatalf("other session %d", w.Code)
	}
	if e = os.WriteFile(stack.PklPath, []byte("changed outside Brine"), 0600); e != nil {
		t.Fatal(e)
	}
	w = call(confirm, c, csrf, url.Values{"review": {id}})
	if w.Code != 200 {
		t.Fatalf("confirm %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	var wire struct {
		Files []struct {
			Path  string `json:"path"`
			Bytes []byte `json:"bytes"`
		} `json:"files"`
	}
	e = json.Unmarshal(bodies[0], &wire)
	mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, f := range wire.Files {
		if f.Path == "compose.pkl" {
			found = string(f.Bytes) == "original"
		}
	}
	if !found {
		t.Fatal("did not send captured bytes")
	}
	if w = call(confirm, c, csrf, url.Values{"review": {id}}); w.Code != 409 {
		t.Fatalf("replay %d", w.Code)
	}
	w = call(prep, c, csrf, nil)
	m = re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal(w.Body.String())
	}
	id = m[1]
	if e = vars.Set("", "REGION", "south", false); e != nil {
		t.Fatal(e)
	}
	if w = call(confirm, c, csrf, url.Values{"review": {id}}); w.Code != 409 {
		t.Fatalf("drift %d", w.Code)
	}
	// A changed source root invalidates the captured review before upload.
	w = call(prep, c, csrf, nil)
	m = re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal(w.Body.String())
	}
	id = m[1]
	st.mu.Lock()
	st.root = filepath.Join(dir, "different&root")
	st.mu.Unlock()
	if w = call(confirm, c, csrf, url.Values{"review": {id}}); w.Code != 409 {
		t.Fatalf("root drift %d", w.Code)
	}
	st.mu.Lock()
	st.root = ""
	st.mu.Unlock()

	// A writer attempting either global or stack settings cannot complete while
	// the final resolve/submission lease is held, including receiver negotiation.
	for _, scope := range []string{"", "app"} {
		w = call(prep, c, csrf, nil)
		m = re.FindStringSubmatch(w.Body.String())
		if len(m) != 2 {
			t.Fatal(w.Body.String())
		}
		id = m[1]
		entered, release := make(chan struct{}), make(chan struct{})
		mu.Lock()
		statusEntered, statusRelease = entered, release
		mu.Unlock()
		result := make(chan *httptest.ResponseRecorder, 1)
		go func(id string) { result <- call(confirm, c, csrf, url.Values{"review": {id}}) }(id)
		<-entered
		started, written := make(chan struct{}), make(chan error, 1)
		go func(scope string) { close(started); written <- vars.Set(scope, "REGION", "after", false) }(scope)
		<-started
		select {
		case err := <-written:
			t.Fatalf("variable write escaped submission lease: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if resp := <-result; resp.Code != 200 {
			t.Fatalf("leased transfer %d %s", resp.Code, resp.Body.String())
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		statusEntered, statusRelease = nil, nil
		mu.Unlock()
	}

	// The cookie is authenticated by middleware, then waits on the stack lock.
	// Logout must linearize first and prevent that pending request's POST.
	w = call(prep, c2, csrf2, nil)
	m = re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal(w.Body.String())
	}
	id = m[1]
	unlock := runner.LockStack("app")
	mu.Lock()
	beforeLogout := len(bodies)
	mu.Unlock()
	pending := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		q := httptest.NewRequest("POST", confirm, strings.NewReader(url.Values{"review": {id}}.Encode()))
		q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		q.Header.Set("X-CSRF-Token", csrf2)
		q.Header.Set("X-Test-Admitted", "1")
		q.AddCookie(c2)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, q)
		pending <- resp
	}()
	<-admitted
	if resp := call("/logout", c2, csrf2, nil); resp.Code != 303 {
		t.Fatalf("logout %d", resp.Code)
	}
	unlock()
	if resp := <-pending; resp.Code == 200 {
		t.Fatal("post-logout transfer accepted")
	}
	mu.Lock()
	afterLogout := len(bodies)
	mu.Unlock()
	if beforeLogout != afterLogout {
		t.Fatal("upload after logout")
	}

	w = call(prep, c, csrf, nil)
	m = re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal(w.Body.String())
	}
	id = m[1]
	// Logout also wins after consumption if receiver negotiation is still in
	// progress and the actual submission lease has not yet been acquired.
	c3, csrf3 := login()
	w3 := call(prep, c3, csrf3, nil)
	m3 := re.FindStringSubmatch(w3.Body.String())
	if len(m3) != 2 {
		t.Fatal(w3.Body.String())
	}
	entered, release := make(chan struct{}), make(chan struct{})
	mu.Lock()
	statusEntered, statusRelease = entered, release
	beforeNegotiationLogout := len(bodies)
	mu.Unlock()
	pendingNegotiation := make(chan *httptest.ResponseRecorder, 1)
	go func() { pendingNegotiation <- call(confirm, c3, csrf3, url.Values{"review": {m3[1]}}) }()
	<-entered
	if resp := call("/logout", c3, csrf3, nil); resp.Code != 303 {
		t.Fatalf("logout during negotiation %d", resp.Code)
	}
	close(release)
	if resp := <-pendingNegotiation; resp.Code == 200 {
		t.Fatal("logged-out negotiation submitted")
	}
	mu.Lock()
	statusEntered, statusRelease = nil, nil
	afterNegotiationLogout := len(bodies)
	mu.Unlock()
	if beforeNegotiationLogout != afterNegotiationLogout {
		t.Fatal("upload after negotiation logout")
	}

	mu.Lock()
	unknown = true
	mu.Unlock()
	w = call(confirm, c, csrf, url.Values{"review": {id}})
	if w.Code != 502 || !strings.Contains(w.Body.String(), "outcome unknown") || !strings.Contains(w.Body.String(), "Do not retry") {
		t.Fatalf("unknown %d %s", w.Code, w.Body.String())
	}
	if w = call(confirm, c, csrf, url.Values{"review": {id}}); w.Code != 409 {
		t.Fatalf("unknown replay %d", w.Code)
	}
	w = call(prep, c, csrf, nil)
	m = re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatal(w.Body.String())
	}
	id = m[1]
	call("/logout", c, csrf, nil)
	if w = call(confirm, c, csrf, url.Values{"review": {id}}); w.Code == 200 {
		t.Fatal("logout accepted")
	}

	// Cookie variable writes wait without holding session authority; cancellation
	// or a completed logout while queued must prevent the eventual mutation.
	for _, mode := range []string{"cancel", "logout"} {
		cookie, token := login()
		lease, err := vars.ReadLease(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			form := url.Values{"name": {"QUEUED"}, "value": {mode}, "action": {"save"}}
			q := httptest.NewRequest("POST", "/variables", strings.NewReader(form.Encode())).WithContext(ctx)
			q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			q.Header.Set("X-CSRF-Token", token)
			q.Header.Set("X-Test-Admitted", "1")
			q.AddCookie(cookie)
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, q)
			result <- resp
		}()
		<-admitted
		if mode == "cancel" {
			cancel()
			select {
			case resp := <-result:
				if resp.Code != 408 {
					t.Fatalf("cancelled writer %d", resp.Code)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled writer did not leave lease wait")
			}
			lease()
		} else {
			if resp := call("/logout", cookie, token, nil); resp.Code != 303 {
				t.Fatalf("logout waiting writer %d", resp.Code)
			}
			lease()
			if resp := <-result; resp.Code != 401 {
				t.Fatalf("logged-out writer %d", resp.Code)
			}
		}
		cancel()
		values, err := vars.Values("")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := values["QUEUED"]; exists {
			t.Fatal("late variable mutation")
		}
	}

	// Staging's variable-lease wait is cancellable without reaching the service.
	cookie4, token4 := login()
	prepared := call(prep, cookie4, token4, nil)
	review4 := re.FindStringSubmatch(prepared.Body.String())
	if len(review4) != 2 {
		t.Fatal(prepared.Body.String())
	}
	lease4, err := vars.ReadLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, stopWait := context.WithCancel(context.Background())
	waitResult := make(chan *httptest.ResponseRecorder, 1)
	mu.Lock()
	beforeWait := len(bodies)
	mu.Unlock()
	go func() {
		q := httptest.NewRequest("POST", confirm, strings.NewReader(url.Values{"review": {review4[1]}}.Encode())).WithContext(waitCtx)
		q.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		q.Header.Set("X-CSRF-Token", token4)
		q.Header.Set("X-Test-Admitted", "1")
		q.AddCookie(cookie4)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, q)
		waitResult <- resp
	}()
	<-admitted
	stopWait()
	select {
	case resp := <-waitResult:
		if resp.Code != 408 {
			t.Fatalf("staging wait cancellation %d", resp.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("staging variable wait stuck")
	}
	lease4()
	mu.Lock()
	afterWait := len(bodies)
	mu.Unlock()
	if beforeWait != afterWait {
		t.Fatal("upload after cancelled lease wait")
	}

	// Admission rejects excess work instead of accumulating unbounded waiters.
	var permits []func()
	for i := 0; i < 8; i++ {
		w := httptest.NewRecorder()
		release, ok := h.admitStaging(w, httptest.NewRequest("POST", prep, nil))
		if !ok {
			t.Fatal("admission unexpectedly full")
		}
		permits = append(permits, release)
	}
	w = httptest.NewRecorder()
	if _, ok := h.admitStaging(w, httptest.NewRequest("POST", prep, nil)); ok || w.Code != 503 {
		t.Fatal("admission not bounded")
	}
	for _, release := range permits {
		release()
	}

	// The legacy stack coordinator cannot be interrupted; admission is bounded,
	// and cancellation is checked before any work after its eventual release.
	cookie, token := login()
	unlock = runner.LockStack("app")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		q := httptest.NewRequest("POST", prep, nil).WithContext(ctx)
		q.AddCookie(cookie)
		q.Header.Set("X-CSRF-Token", token)
		q.Header.Set("X-Test-Admitted", "1")
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, q)
		result <- resp
	}()
	<-admitted
	cancel()
	unlock()
	if resp := <-result; resp.Code != 408 {
		t.Fatalf("cancelled stack waiter %d", resp.Code)
	}
}
