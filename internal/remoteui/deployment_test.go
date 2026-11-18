package remoteui

import (
	"bytes"
	"context"
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
	"sync/atomic"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRemoteDeploymentReviewStageCreateStatus(t *testing.T) {
	fs, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := fs.Create("synthetic", "original-reviewed-project")
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	const socket = "unix:///run/docker.sock"
	const engine = "11111111-1111-4111-8111-111111111111"
	const epoch = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	stage := strings.Repeat("1", 32)
	gold, err := os.ReadFile("../dill/testdata/preflight-v1/preview-up-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Stdout json.RawMessage }
	if json.Unmarshal(gold, &golden) != nil {
		t.Fatal("invalid fixture")
	}
	plan := bytes.ReplaceAll(golden.Stdout, []byte("sha256:"+strings.Repeat("1", 64)), []byte(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(socket)))))
	var calls atomic.Int32
	var manifest atomic.Value
	var operationID atomic.Value
	enabled := atomic.Bool{}
	enabled.Store(true)
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing bearer credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/session":
			var q struct{ Nonce string }
			if json.NewDecoder(r.Body).Decode(&q) != nil {
				t.Error("invalid challenge")
				return
			}
			p := agentclient.Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(pub)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(pub), EngineID: engine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: "daemon", Epoch: epoch, Nonce: q.Nonce}
			msg, _ := agentclient.SessionBytes(p)
			p.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))
			raw, _ := json.Marshal(p)
			fmt.Fprintf(w, `{"version":"brine.agent.read/1","requestId":%q,"data":%s}`, r.Header.Get("X-Request-Id"), raw)
		case "/v1/snapshots":
			type file struct {
				Path   string `json:"path"`
				Length int    `json:"length"`
				SHA256 string `json:"sha256"`
			}
			var req struct {
				Entrypoint string
				Files      []struct {
					file
					Bytes []byte
				}
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid snapshot")
				return
			}
			files := make([]file, 0, len(req.Files))
			found := false
			for _, row := range req.Files {
				files = append(files, row.file)
				if row.Path == req.Entrypoint {
					found = string(row.Bytes) == "original-reviewed-project"
				}
			}
			if !found {
				t.Error("uploaded bytes were not the reviewed capture")
			}
			raw, _ := json.Marshal(struct {
				Schema     string `json:"schema"`
				Entrypoint string `json:"entrypoint"`
				Files      []file `json:"files"`
			}{"dill.snapshot.manifest.v1", req.Entrypoint, files})
			manifest.Store(fmt.Sprintf(`{"stage_id":%q,"manifest":%s,"manifest_sha256":"%x"}`, stage, raw, sha256.Sum256(raw)))
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"status":"staged","stage_id":%q}`, stage)
		case "/v1/snapshots/" + stage:
			fmt.Fprint(w, manifest.Load().(string))
		case "/v1/deploy/preview":
			if enabled.Load() {
				w.Header().Set("X-Dill-Create-Enabled", "1")
			}
			_, _ = w.Write(plan)
		case "/v1/deploy/apply":
			calls.Add(1)
			var f map[string]string
			if json.NewDecoder(r.Body).Decode(&f) != nil || f["stageId"] != stage || f["confirm"] != "create" || f["stack"] != "synthetic" {
				t.Error("invalid create request")
			}
			operationID.Store(f["requestId"])
			// Simulate an ambiguous response; the durable status is discoverable separately.
			w.WriteHeader(502)
		default:
			id, _ := operationID.Load().(string)
			if id == "" || r.URL.Path != "/v1/deploy/operations/"+id {
				t.Error("unexpected route")
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"succeeded","effects":"complete","operationId":%q}`, id, "op_"+id)
		}
	}))
	defer agent.Close()
	pin := sha256.Sum256(agent.Certificate().Raw)
	bundle := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":%q,"generation":1,"engineKind":"docker","engineSocket":%q,"daemonId":"daemon"}`, hex.EncodeToString(pin[:]), token, hex.EncodeToString(pub), engine, socket)
	reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "targets"))
	if err != nil {
		t.Fatal(err)
	}
	row := targetregistry.Record{ID: "demo", Revision: 1, HTTPSAddress: strings.TrimPrefix(agent.URL, "https://"), HTTPSPairing: targetregistry.PairingSecret(bundle)}
	if err := reg.AddContext(context.Background(), row, row); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewDeployment(a, reg, fs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	login := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=secret"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.HandleLogin(login, r)
	cookie := login.Result().Cookies()[0]
	handler := h.Protected()
	invoke := func(method, path string, params url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(params.Encode()))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("credential in response")
		}
		return w
	}
	field := func(w *httptest.ResponseRecorder, name string) string {
		t.Helper()
		m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
		if len(m) != 2 {
			t.Fatalf("missing %s, HTTP %d: %s", name, w.Code, w.Body.String())
		}
		return m[1]
	}
	csrf := field(invoke("GET", deployPath+"?host=demo", nil), "_csrf")
	params := url.Values{"_csrf": {csrf}, "host": {"demo"}, "stack": {"synthetic"}}
	w := invoke("POST", deployPath+"/review", params)
	review := field(w, "id")
	if strings.Contains(w.Body.String(), "original-reviewed-project") {
		t.Fatal("source contents in HTML")
	}
	if err := os.WriteFile(stack.PklPath, []byte("changed-later"), 0600); err != nil {
		t.Fatal(err)
	}
	params.Set("id", review)
	params.Set("confirm", "transfer")
	w = invoke("POST", deployPath+"/stage", params)
	id := field(w, "id")
	if calls.Load() != 0 || !strings.Contains(w.Body.String(), "Create stack") {
		t.Fatal("stage unexpectedly applied")
	}
	params = url.Values{"id": {id}, "_csrf": {"wrong"}, "confirm": {"create"}}
	if w := invoke("POST", deployPath+"/create", params); w.Code == 200 || calls.Load() != 0 {
		t.Fatal("invalid CSRF admitted")
	}
	params.Set("_csrf", csrf)
	if w := invoke("POST", deployPath+"/create", params); !strings.Contains(w.Body.String(), "No retry was sent") || calls.Load() != 1 {
		t.Fatal("lost response not held", w.Code)
	}
	if w := invoke("POST", deployPath+"/create", params); w.Code != 409 || calls.Load() != 1 {
		t.Fatal("create replayed", w.Code)
	}
	if w := invoke("GET", deployPath+"?host=demo&id="+id, nil); !strings.Contains(w.Body.String(), "State succeeded") || calls.Load() != 1 {
		t.Fatal("status did not reconcile request", w.Code)
	}
}
