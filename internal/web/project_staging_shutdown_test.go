package web

import (
	"context"
	"errors"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/store"
)

func TestStagingShutdownLinearization(t *testing.T) {
	for _, submitFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "close-wins-negotiation", true: "submission-wins"}[submitFirst], func(t *testing.T) {
			dir := t.TempDir()
			st, err := store.NewFSStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.Create("app", "source"); err != nil {
				t.Fatal(err)
			}
			vars, err := configvars.New(dir, st)
			if err != nil {
				t.Fatal(err)
			}
			runner := dill.New("unused")
			runner.Variables = vars
			ag := agent.NewLocalAgent(nil, runner)
			sockdir, err := os.MkdirTemp("", "brsd-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(sockdir)
			sock := filepath.Join(sockdir, "s")
			listener, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			statusEntered, statusRelease := make(chan struct{}), make(chan struct{})
			postEntered, postRelease := make(chan struct{}), make(chan struct{})
			var posts atomic.Int32
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/health":
					io.WriteString(w, `{"api_version":"v1","capabilities":["health","status","snapshot-staging"]}`)
				case "/v1/status":
					close(statusEntered)
					<-statusRelease
					io.WriteString(w, `{"stack":"app","engine":"docker","containers":[]}`)
				case "/v1/snapshots":
					posts.Add(1)
					close(postEntered)
					<-postRelease
					w.WriteHeader(201)
					io.WriteString(w, `{"status":"staged","stage_id":"0123456789abcdef0123456789abcdef"}`)
				}
			})}
			go srv.Serve(listener)
			defer srv.Close()
			a, err := auth.New(auth.Config{User: "admin", Password: "test-only-password"})
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.StagingSocket = sock
			cfg.StagingIdentity = "fixture"
			h := New(ag, st, nil, nil, "docker", false, a, 0)
			h.EnableLocalStaging(cfg)
			defer h.CloseStaging()
			r := chi.NewRouter()
			r.Use(a.Middleware)
			r.Get("/csrf", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, a.CSRFFor(r)) })
			Mount(r, h, httpx.Timeouts{})
			f := url.Values{"user": {"admin"}, "password": {"test-only-password"}}
			login := httptest.NewRequest("POST", "/login", strings.NewReader(f.Encode()))
			login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			lw := httptest.NewRecorder()
			a.HandleLogin(lw, login)
			cookie := lw.Result().Cookies()[0]
			req := httptest.NewRequest("GET", "/csrf", nil)
			req.AddCookie(cookie)
			cw := httptest.NewRecorder()
			r.ServeHTTP(cw, req)
			csrf := cw.Body.String()
			call := func(path string, form url.Values) *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("X-CSRF-Token", csrf)
				req.AddCookie(cookie)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				return w
			}
			expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancelExpired()
			expiredReq := httptest.NewRequest("POST", "/web/stacks/app/staging/prepare", nil).WithContext(expired)
			expiredReq.AddCookie(cookie)
			expiredReq.Header.Set("X-CSRF-Token", csrf)
			expiredResp := httptest.NewRecorder()
			r.ServeHTTP(expiredResp, expiredReq)
			if expiredResp.Code != 408 || posts.Load() != 0 {
				t.Fatalf("expired request: %d, posts %d", expiredResp.Code, posts.Load())
			}
			p := call("/web/stacks/app/staging/prepare", nil)
			if p.Code != 200 {
				t.Fatalf("prepare %d %s", p.Code, p.Body.String())
			}
			match := regexp.MustCompile(`name="review" value="([a-f0-9]+)"`).FindStringSubmatch(p.Body.String())
			if len(match) != 2 {
				t.Fatal("missing review")
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- call("/web/stacks/app/staging/confirm", url.Values{"review": {match[1]}}) }()
			select {
			case <-statusEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("status not entered")
			}
			if submitFirst {
				close(statusRelease)
				select {
				case <-postEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("POST not entered")
				}
			}
			// Several concurrent close calls all return even while the fake server is held.
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); h.CloseStaging() }()
			}
			closed := make(chan struct{})
			go func() { wg.Wait(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("close blocked on network")
			}
			if _, err := h.stagingStore.Confirm(context.Background(), "session", "id", projectreview.Current{}); !errors.Is(err, projectreview.ErrClosed) {
				t.Fatalf("store not closed: %v", err)
			}
			if !submitFirst {
				close(statusRelease)
			}
			select {
			case w := <-result:
				if w.Code == 200 {
					t.Fatal("close reported success")
				}
				if submitFirst && !strings.Contains(w.Body.String(), "outcome unknown") {
					t.Fatalf("interrupted POST: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("request not cancelled")
			}
			if submitFirst {
				close(postRelease)
				if posts.Load() != 1 {
					t.Fatalf("expected one started POST: %d", posts.Load())
				}
			} else {
				close(postRelease)
				if posts.Load() != 0 {
					t.Fatalf("POST after close: %d", posts.Load())
				}
			}
		})
	}
}
