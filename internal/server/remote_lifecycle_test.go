package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRemoteDisabledAndStartupFailClosed(t *testing.T) {
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	cfg := config.Default()
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err := s.prepareRemote(nil, nil); err == nil {
		t.Fatal("enabled without opt in")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/remote", nil)
	req.Header.Set("Accept", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Fatal("disabled route")
	}
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := s.prepareRemote(nil, nil); err == nil {
		t.Fatal("uninitialized registry accepted")
	}
	if _, err := os.Lstat(filepath.Join(cfg.RemoteRegistryDir, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("startup created lock: %v", err)
	}
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	noauth, _ := auth.New(auth.Config{NoAuth: true})
	s2 := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, noauth)
	if err := s2.prepareRemote(nil, nil); err == nil {
		t.Fatal("no-auth accepted")
	}
}

type countingBody struct {
	r io.Reader
	n int
}

func (b *countingBody) Read(p []byte) (int, error) { n, err := b.r.Read(p); b.n += n; return n, err }
func (*countingBody) Close() error                 { return nil }

func TestRemoteMountLeavesLocalRoutingAndAuthUnchanged(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	check := func(path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		return w.Code
	}
	beforeHealth, beforeProtected, beforePrefix := check("/api/health"), check("/api/stacks"), check("/remoteish")
	if err := s.prepareRemote(nil, nil); err != nil {
		t.Fatal(err)
	}
	if check("/api/health") != beforeHealth || check("/api/stacks") != beforeProtected || check("/remoteish") != beforePrefix {
		t.Fatal("local routing or authentication changed")
	}
}

func TestRemoteBodyLimitPrecedesGlobalAuthentication(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret"})
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err := s.prepareRemote(nil, nil); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	s.router.ServeHTTP(lw, login)
	cookies := lw.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing cookie")
	}
	body := &countingBody{r: strings.NewReader("padding=" + strings.Repeat("x", 100000))}
	req := httptest.NewRequest("POST", "/remote/enrollment/review", nil)
	req.Body = body
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code < 400 || body.n > (32<<10)+1 {
		t.Fatalf("status %d bytes read %d", w.Code, body.n)
	}
}

func TestRemoteCookieExpires(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	a, _ := auth.New(auth.Config{User: "admin", Password: "secret", SessionTTL: 20 * time.Millisecond})
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err := s.prepareRemote(nil, nil); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Post(ts.URL+"/login", "application/x-www-form-urlencoded", strings.NewReader("user=admin&password=secret"))
	if err != nil {
		t.Fatal(err)
	}
	cookies := resp.Cookies()
	resp.Body.Close()
	if len(cookies) == 0 {
		t.Fatal("missing cookie")
	}
	time.Sleep(50 * time.Millisecond)
	req, _ := http.NewRequest("GET", ts.URL+"/remote", nil)
	req.AddCookie(cookies[0])
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("expired session accepted")
	}
}
