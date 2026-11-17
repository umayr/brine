package remoteui

// This fixture mounts the otherwise unmounted reveal handler only for tests.
// Its agent and signer are synthetic; it never opens SSH or an engine.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remotereveal"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

type e2eSession struct {
	calls     *atomic.Int32
	request   *atomic.Value
	failure   *atomic.Bool
	block     *atomic.Bool
	cancelled *atomic.Int32
	record    targetregistry.Record
	key       ed25519.PublicKey
}

func (s *e2eSession) Epoch() string { return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }
func (s *e2eSession) Close() error  { return nil }
func (s *e2eSession) Reveal(ctx context.Context, epoch, id string, grant agentclient.Grant) (agentclient.RevealResult, error) {
	s.calls.Add(1)
	s.request.Store(grant.RequestID)
	if epoch != s.Epoch() || id != strings.Repeat("a", 64) || agentclient.VerifyGrant(s.key, grant, s.record.AgentInstallationID, s.record.EngineID, s.record.Generation, epoch, id, uint64(time.Now().Unix())) != nil {
		return agentclient.RevealResult{}, errors.New("synthetic grant binding failed")
	}
	if s.block.Load() {
		<-ctx.Done()
		s.cancelled.Add(1)
		return agentclient.RevealResult{}, ctx.Err()
	}
	if s.failure.Load() {
		return agentclient.RevealResult{}, errors.New("synthetic agent failure")
	}
	result := agentclient.RevealResult{ContainerID: id}
	result.Env = append(result.Env, struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{"KEY", "secret-value"})
	return result, ctx.Err()
}

type auditedWriter struct {
	http.ResponseWriter
	audit    *revealaudit.Audit
	baseline int
	status   int
	checked  bool
}

func (w *auditedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *auditedWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *auditedWriter) Write(p []byte) (int, error) {
	if !w.checked && (w.status == 0 || w.status == http.StatusOK) {
		entries, err := w.audit.Entries(context.Background())
		if err != nil || len(entries) != w.baseline+1 {
			return 0, errors.New("new audit missing before delivery")
		}
		last := entries[w.baseline]
		if last.Outcome != revealaudit.Succeeded || last.Intent.Correlation == "" || last.Intent.TargetID != "server" || last.Intent.ContainerID != strings.Repeat("a", 64) || last.Intent.AdminID != "admin" {
			return 0, errors.New("audit mismatch before delivery")
		}
		w.Header().Set("X-Fixture-Audit-Before-Write", "verified")
		w.checked = true
	}
	return w.ResponseWriter.Write(p)
}

func TestRealRevealE2EFixture(t *testing.T) {
	dir := os.Getenv("BRINE_REAL_REVEAL_DIR")
	if dir == "" {
		t.Skip("browser fixture only")
	}
	ttl := 5 * time.Minute
	if os.Getenv("BRINE_REAL_REVEAL_SHORT_TTL") == "1" {
		ttl = 8 * time.Second
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "fixture-password", APIToken: "fixture-bearer", SessionTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(filepath.Join(dir, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	rec := enrollmentRecord(t)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	hash := sha256.Sum256(key.Public().(ed25519.PublicKey))
	rec.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(hash[:])
	if err = reg.Add(rec, rec); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "audit"), 0700); err != nil {
		t.Fatal(err)
	}
	audit, err := revealaudit.Initialize(context.Background(), filepath.Join(dir, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	var calls, opens, cancelled atomic.Int32
	var failure, block atomic.Bool
	var request atomic.Value
	request.Store("")
	e := &remotereveal.Executor{Registry: reg, Audit: audit, Signer: reviewSigner{key}, Open: func(_ context.Context, actual targetregistry.Record, public ed25519.PublicKey) (remotereveal.Session, error) {
		opens.Add(1)
		if actual != rec || !bytes.Equal(public, key.Public().(ed25519.PublicKey)) {
			return nil, errors.New("synthetic opener binding failed")
		}
		return &e2eSession{calls: &calls, request: &request, failure: &failure, block: &block, cancelled: &cancelled, record: rec, key: key.Public().(ed25519.PublicKey)}, nil
	}}
	reveal, err := NewRevealHandler(a, reg, e)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := New(&fake{}, a)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.HandleLogin)
	mux.Handle("/logout", a.Middleware(http.HandlerFunc(a.HandleLogout)))
	mux.Handle("/csrf", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		_, _ = fmt.Fprint(w, a.CSRFFor(r))
	})))
	mux.Handle("/fixture/target", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		_ = json.NewEncoder(w).Encode(rec)
	})))
	mux.Handle("/fixture/request-id", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		_, _ = fmt.Fprint(w, request.Load().(string))
	})))
	mux.Handle("/fixture/stats", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		entries, err := audit.Entries(r.Context())
		if err != nil {
			http.Error(w, "audit unavailable", 500)
			return
		}
		outcomes := make([]revealaudit.Outcome, 0, len(entries))
		for _, entry := range entries {
			outcomes = append(outcomes, entry.Outcome)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"opens": opens.Load(), "calls": calls.Load(), "audit": len(entries), "outcomes": outcomes, "cancelled": cancelled.Load()})
	})))
	mux.Handle("/fixture/block", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		block.Store(true)
	})))
	mux.Handle("/fixture/failure", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		failure.Store(true)
	})))
	mux.Handle("/fixture/mutate", a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CookieAuthenticated(r) {
			http.Error(w, "forbidden", 403)
			return
		}
		next := rec
		next.Revision++
		next.DaemonID = "changed-daemon"
		if err := reg.Update(rec, next, targetregistry.ReviewedUpdate{Previous: rec, Next: next}); err != nil {
			http.Error(w, "mutation failed", 500)
			return
		}
	})))
	mux.Handle("/remote/session", remote.Protected())
	mux.Handle("/remote/reveal/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/remote/reveal/approve" {
			reveal.Protected().ServeHTTP(w, r)
			return
		}
		entries, err := audit.Entries(r.Context())
		if err != nil {
			http.Error(w, "audit unavailable", 500)
			return
		}
		reveal.Protected().ServeHTTP(&auditedWriter{ResponseWriter: w, audit: audit, baseline: len(entries)}, r)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err = os.WriteFile(filepath.Join(dir, "address"), []byte("http://"+listener.Addr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
