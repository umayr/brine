package remoteui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestHTTPSPairAndInventory(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	const engine = "11111111-1111-4111-8111-111111111111"
	const epoch = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch r.URL.Path {
		case "/v1/session":
			var q struct {
				Nonce string `json:"nonce"`
			}
			if json.NewDecoder(r.Body).Decode(&q) != nil {
				t.Error("invalid nonce")
				return
			}
			h := sha256.Sum256(pub)
			proof := agentclient.Proof{AgentID: hex.EncodeToString(h[:]), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: engine, Generation: 1, EngineKind: "docker", EngineSocket: "unix:///run/docker.sock", DaemonID: "daemon", Epoch: epoch, Nonce: q.Nonce}
			msg, _ := agentclient.SessionBytes(proof)
			proof.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
			b, _ := json.Marshal(proof)
			data = string(b)
		case "/v1/inventory":
			data = `{"items":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"demo","stack":"s","service":"v","state":"running","image":"i"}],"nextCursor":null,"observedAt":"2025-01-01T00:00:00Z"}`
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"version":"brine.agent.read/1","requestId":%q,"data":%s}`, r.Header.Get("X-Request-Id"), data)
	}))
	defer agent.Close()
	pin := sha256.Sum256(agent.Certificate().Raw)
	bundle := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":%q,"generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"daemon"}`, hex.EncodeToString(pin[:]), token, hex.EncodeToString(pub), engine)
	dir := filepath.Join(t.TempDir(), "targets")
	reg, err := targetregistry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewRegistryLeaseProvider(reg, remoteagent.Config{LocalDir: t.TempDir(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := NewReadBackend(reg, provider)
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(backend, a)
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Protected()
	login := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, req)
	cookie := login.Result().Cookies()[0]
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	page := get("/remote")
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(m) != 2 {
		t.Fatal("no csrf")
	}
	post := func(csrf string) *httptest.ResponseRecorder {
		values := url.Values{"_csrf": {csrf}, "id": {"test"}, "address": {strings.TrimPrefix(agent.URL, "https://")}, "bundle": {bundle}}
		req := httptest.NewRequest("POST", "/remote/pair", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := post("invalid"); w.Code == http.StatusSeeOther {
		t.Fatal("invalid csrf accepted")
	}
	if w := post(m[1]); w.Code != http.StatusSeeOther || strings.Contains(w.Body.String(), token) {
		t.Fatalf("pair status %d", w.Code)
	}
	if w := get("/remote"); !strings.Contains(w.Body.String(), "test") || strings.Contains(w.Body.String(), token) {
		t.Fatal("bad list")
	}
	if w := get("/remote/inventory?host=test"); !strings.Contains(w.Body.String(), "demo") || strings.Contains(w.Body.String(), token) {
		t.Fatalf("bad inventory: %s", w.Body.String())
	}
	st, err := os.Stat(filepath.Join(dir, "targets.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("registry not private")
	}
	forget := func(csrf string) *httptest.ResponseRecorder {
		v := url.Values{"_csrf": {csrf}, "id": {"test"}, "confirm": {"yes"}}
		r := httptest.NewRequest("POST", "/remote/forget", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := forget("invalid"); w.Code == http.StatusSeeOther {
		t.Fatal("forgot target without CSRF")
	}
	if w := forget(m[1]); w.Code != http.StatusSeeOther {
		t.Fatalf("forget status %d", w.Code)
	}
	if w := get("/remote/inventory?host=test"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("forgotten target still readable: %d", w.Code)
	}
}
