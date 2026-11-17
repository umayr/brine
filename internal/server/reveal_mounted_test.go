package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

type mountedSigner struct{ key ed25519.PrivateKey }

func (s mountedSigner) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }
func (s mountedSigner) Sign(ctx context.Context, b []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ed25519.Sign(s.key, b), nil
}

type mountedRevealSession struct {
	block   bool
	entered chan struct{}
	closed  chan struct{}
	calls   *atomic.Int32
}

func (s *mountedRevealSession) Epoch() string { return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }
func (s *mountedRevealSession) Close() error  { close(s.closed); return nil }
func (s *mountedRevealSession) Reveal(ctx context.Context, _ string, id string, _ agentclient.Grant) (agentclient.RevealResult, error) {
	s.calls.Add(1)
	close(s.entered)
	if s.block {
		<-ctx.Done()
		return agentclient.RevealResult{}, ctx.Err()
	}
	r := agentclient.RevealResult{ContainerID: id}
	r.Env = append(r.Env, struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{"KEY", "synthetic-value"})
	return r, nil
}

func TestMountedRevealSuccessAndShutdown(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "shutdown"}[block], func(t *testing.T) {
			cfg := config.Default()
			cfg.RemoteEnabled = true
			cfg.RemoteRegistryDir = privateTestDir(t)
			cfg.RemoteForwardDir = privateTestDir(t)
			cfg.RemoteTimeout = time.Second
			cfg.RemoteRevealEnabled = true
			cfg.RemoteRevealAuditDir = privateTestDir(t)
			cfg.RemoteRevealSeedPath = filepath.Join(privateTestDir(t), "seed")
			if err := os.WriteFile(cfg.RemoteRevealSeedPath, []byte(strings.Repeat("0", 64)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
				t.Fatal(err)
			}
			reg, err := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
			if err != nil {
				t.Fatal(err)
			}
			rec := syntheticRecord()
			pub := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
			hash := sha256.Sum256(pub)
			rec.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
			if err = reg.Add(rec, rec); err != nil {
				t.Fatal(err)
			}
			audit, err := revealaudit.Initialize(context.Background(), cfg.RemoteRevealAuditDir)
			if err != nil {
				t.Fatal(err)
			}
			a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
			entered := make(chan struct{})
			closed := make(chan struct{})
			var calls atomic.Int32
			s.testRevealFactory = func(a *auth.Authenticator, r *targetregistry.Registry) (*remoteui.RevealHandler, error) {
				e := &remotereveal.Executor{Registry: r, Audit: audit, Signer: mountedSigner{ed25519.NewKeyFromSeed(make([]byte, 32))}, Open: func(context.Context, targetregistry.Record, ed25519.PublicKey) (remotereveal.Session, error) {
					return &mountedRevealSession{block: block, entered: entered, closed: closed, calls: &calls}, nil
				}}
				return remoteui.NewRevealHandler(a, r, e)
			}
			if err = s.prepareRemote(testLease{reg}, testObserver{}); err != nil {
				t.Fatal(err)
			}
			defer s.remoteCancel()
			ts := httptest.NewServer(s.router)
			defer ts.Close()
			client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			request := func(method, path string, form url.Values, c *http.Cookie) (int, string, http.Header, error) {
				req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if c != nil {
					req.AddCookie(c)
				}
				resp, e := client.Do(req)
				if e != nil {
					return 0, "", nil, e
				}
				defer resp.Body.Close()
				b, e := io.ReadAll(resp.Body)
				return resp.StatusCode, string(b), resp.Header, e
			}
			_, _, hdr, err := request("POST", "/login", url.Values{"user": {"admin"}, "password": {"secret"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			cookies := (&http.Response{Header: hdr}).Cookies()
			if len(cookies) == 0 {
				t.Fatal("no session")
			}
			cookie := cookies[0]
			_, page, _, err := request("GET", "/remote/enrollment", nil, cookie)
			if err != nil {
				t.Fatal(err)
			}
			m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page)
			if len(m) != 2 {
				t.Fatal("no csrf")
			}
			csrf := m[1]
			id := strings.Repeat("a", 64)
			status, review, _, err := request("POST", "/remote/reveal/review", url.Values{"_csrf": {csrf}, "host": {rec.ID}, "container": {id}}, cookie)
			if err != nil || status != 200 {
				t.Fatalf("review %d %v", status, err)
			}
			match := regexp.MustCompile(`token:("[^"]+")`).FindStringSubmatch(review)
			if len(match) != 2 {
				t.Fatal("missing token")
			}
			token, err := strconv.Unquote(match[1])
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"_csrf": {csrf}, "token": {token}, "confirm": {"yes"}}
			type result struct {
				status int
				body   string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				code, body, _, e := request("POST", "/remote/reveal/approve", form, cookie)
				done <- result{code, body, e}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("agent not entered")
			}
			if block {
				s.remoteCancel()
			}
			select {
			case out := <-done:
				if !block && (out.err != nil || out.status != 200 || !strings.Contains(out.body, "synthetic-value")) {
					t.Fatalf("approval %d %v %q", out.status, out.err, out.body)
				}
				if block && strings.Contains(out.body, "synthetic-value") {
					t.Fatal("shutdown leaked value")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request did not end")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("session not closed")
			}
			entries, err := audit.Entries(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !block {
				if len(entries) != 1 || entries[0].Outcome != revealaudit.Succeeded {
					t.Fatal("missing durable success")
				}
				status, _, _, err = request("POST", "/remote/reveal/approve", form, cookie)
				if err != nil || status != http.StatusForbidden || calls.Load() != 1 {
					t.Fatalf("replay %d %v", status, err)
				}
			}
		})
	}
}
