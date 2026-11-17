package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRevealMountedHTTPBoundaries(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RemoteRevealEnabled = true
	cfg.RemoteRevealSeedPath = filepath.Join(privateTestDir(t), "seed")
	cfg.RemoteRevealAuditDir = privateTestDir(t)
	if err = os.WriteFile(cfg.RemoteRevealSeedPath, []byte(strings.Repeat("0", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = revealaudit.Initialize(context.Background(), cfg.RemoteRevealAuditDir); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", APIToken: "token", SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err = s.prepareRemote(testLease{reg}, testObserver{}); err != nil {
		t.Fatal(err)
	}
	defer s.remoteCancel()
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, data url.Values, cookie *http.Cookie, bearer bool) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(data.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer token")
		}
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	_, _, headers := request("POST", "/login", url.Values{"user": {"admin"}, "password": {"secret"}}, nil, false)
	cookies := (&http.Response{Header: headers}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("login failed")
	}
	cookie := cookies[0]
	_, page, _ := request("GET", "/remote/enrollment", nil, cookie, false)
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)
	if len(m) != 2 {
		t.Fatal("no csrf")
	}
	form := url.Values{"_csrf": {m[1]}, "host": {"missing"}, "container": {strings.Repeat("a", 64)}}
	if status, _, _ := request("POST", "/remote/reveal/review", form, cookie, true); status != http.StatusForbidden {
		t.Fatalf("bearer %d", status)
	}
	if status, _, _ := request("POST", "/remote/reveal/review", url.Values{"_csrf": {"bad"}}, cookie, false); status != http.StatusForbidden {
		t.Fatalf("csrf %d", status)
	}
	form.Set("padding", strings.Repeat("x", 33000))
	if status, _, _ := request("POST", "/remote/reveal/review", form, cookie, false); status < 400 {
		t.Fatalf("oversize %d", status)
	}
	for _, path := range []string{"/remote/reveal", "/remote/reveal/other", "/remote/review", "/remote/enroll"} {
		if status, _, _ := request("POST", path, url.Values{"_csrf": {m[1]}}, cookie, false); status != http.StatusNotFound {
			t.Errorf("%s: %d", path, status)
		}
	}
}
