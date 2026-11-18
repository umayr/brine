package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/crypto/ssh"
)

type testObserver struct{}

func (testObserver) Observe(_ context.Context, r targetregistry.Record) (targetregistry.Record, error) {
	return r, nil
}

type testLease struct{ r *targetregistry.Registry }

func (p testLease) Acquire(ctx context.Context, id string) (context.Context, remoteui.ReadSession, func() error, error) {
	_, lease, err := p.r.AcquireRead(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	return ctx, testSession{}, lease.Close, nil
}

type testSession struct{}

func (testSession) Inventory(context.Context, int) ([]agentclient.InventoryItem, error) {
	return []agentclient.InventoryItem{{ID: strings.Repeat("a", 64), Name: "synthetic-container", Stack: "stack", Service: "service", State: "running", Image: "image"}}, nil
}
func (testSession) Inspect(context.Context, string) (agentclient.InspectResult, error) {
	return agentclient.InspectResult{}, fmt.Errorf("unused")
}
func (testSession) Logs(context.Context, string, agentclient.LogOptions, func(context.Context, agentclient.LogEvent) error) error {
	return fmt.Errorf("unused")
}
func syntheticRecord() targetregistry.Record {
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	hash := sha256.Sum256(key)
	sshkey, _ := ssh.NewPublicKey(key)
	pin := ssh.FingerprintSHA256(sshkey)
	return targetregistry.Record{Revision: 1, RemoteSocket: "/run/brine.sock", AgentPublicKey: base64.RawURLEncoding.EncodeToString(key), AgentFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:]), EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Generation: 1, RevealKeyFingerprint: pin, ID: "example", Host: "host.example", Port: 22, User: "operator", KeyPath: "/private/key", HostPublicKey: strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(sshkey)), "\n"), HostKeySHA256: pin, AgentInstallationID: hex.EncodeToString(hash[:]), EngineID: "11111111-1111-4111-8111-111111111111"}
}
func privateTestDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRemoteRealServerEnrollmentAndSecurity(t *testing.T) {
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
	a, err := auth.New(auth.Config{User: "admin", Password: "secret", APIToken: "token", SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err := s.prepareRemote(testLease{reg}, testObserver{}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, form url.Values, cookie *http.Cookie, bearer bool) (int, string, http.Header) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer token")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	status, _, _ := request("GET", "/remote", nil, nil, false)
	if status == 200 {
		t.Fatal("unauthenticated remote")
	}
	status, _, _ = request("GET", "/remote", nil, nil, true)
	if status != http.StatusForbidden {
		t.Fatalf("bearer: %d", status)
	}
	status, _, headers := request("POST", "/login", url.Values{"user": {"admin"}, "password": {"secret"}}, nil, false)
	if status != http.StatusSeeOther && status != http.StatusFound {
		t.Fatalf("login %d", status)
	}
	cookies := (&http.Response{Header: headers}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookie")
	}
	cookie := cookies[0]
	status, body, _ := request("GET", "/remote/enrollment", nil, cookie, false)
	if status != 200 {
		t.Fatalf("enrollment %d %s", status, body)
	}
	csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(csrf) != 2 {
		t.Fatal("missing csrf")
	}
	status, _, _ = request("POST", "/remote/enrollment/review", url.Values{"_csrf": {"bad"}}, cookie, false)
	if status != http.StatusForbidden {
		t.Fatalf("csrf: %d", status)
	}
	status, _, _ = request("POST", "/remote/enrollment/review", url.Values{"_csrf": {csrf[1]}, "padding": {strings.Repeat("x", 33000)}}, cookie, false)
	if status == 200 {
		t.Fatal("oversized form accepted")
	}
	v := syntheticRecord()
	fields := reflect.ValueOf(v)
	typ := fields.Type()
	form := url.Values{"_csrf": {csrf[1]}}
	for i := 0; i < fields.NumField(); i++ {
		name := typ.Field(i).Tag.Get("json")
		if !strings.Contains(name, ",") {
			form.Set(name, fmt.Sprint(fields.Field(i).Interface()))
		}
	}
	status, body, _ = request("POST", "/remote/enrollment/review", form, cookie, false)
	if status != 200 {
		t.Fatalf("review %d %s", status, body)
	}
	token := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(token) != 2 {
		t.Fatalf("review missing token: %s", body)
	}
	status, body, _ = request("POST", "/remote/enrollment/approve", url.Values{"_csrf": {csrf[1]}, "token": {token[1]}, "confirm": {"yes"}}, cookie, false)
	if status != 200 || !strings.Contains(body, "Enrollment approved") {
		t.Fatalf("approve %d %s", status, body)
	}
	status, body, _ = request("GET", "/remote", nil, cookie, false)
	if status != 200 || !strings.Contains(body, "example") {
		t.Fatalf("hosts %d %s", status, body)
	}
	status, body, _ = request("GET", "/remote/inventory?host=example", nil, cookie, false)
	if status != 200 || !strings.Contains(body, "synthetic-container") {
		t.Fatalf("inventory %d %s", status, body)
	}
	for _, path := range []string{"/remote/reveal", "/remote/unknown", "/remote/deploy"} {
		status, _, _ = request("GET", path, nil, cookie, false)
		if status < 400 {
			t.Errorf("%s: %d", path, status)
		}
		if path == "/remote/reveal" && status != http.StatusNotFound {
			t.Errorf("reveal: %d", status)
		}
	}
	// A fresh server and authenticator must reopen persisted enrollment, but not
	// accept the prior process's in-memory session.
	restartedAuth, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, restartedAuth)
	reopened, err := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.prepareRemote(testLease{reopened}, testObserver{}); err != nil {
		t.Fatal(err)
	}
	rw := httptest.NewRecorder()
	rr := httptest.NewRequest("GET", "/remote", nil)
	rr.AddCookie(cookie)
	restarted.router.ServeHTTP(rw, rr)
	if rw.Code == http.StatusOK {
		t.Fatal("old session survived restart")
	}
	login := httptest.NewRecorder()
	lr := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	restarted.router.ServeHTTP(login, lr)
	if len(login.Result().Cookies()) == 0 {
		t.Fatal("restart login failed")
	}
	rw = httptest.NewRecorder()
	rr = httptest.NewRequest("GET", "/remote", nil)
	rr.AddCookie(login.Result().Cookies()[0])
	restarted.router.ServeHTTP(rw, rr)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "example") {
		t.Fatalf("restart host %d %s", rw.Code, rw.Body.String())
	}

	status, _, _ = request("POST", "/logout", url.Values{"_csrf": {csrf[1]}}, cookie, false)
	if status >= 400 {
		t.Fatalf("logout %d", status)
	}
	status, _, _ = request("GET", "/remote", nil, cookie, false)
	if status == 200 {
		t.Fatal("session survives logout")
	}
}
