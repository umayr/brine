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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRemoteDeploymentReviewStageCreateStatus(t *testing.T) {
	data := t.TempDir()
	fs, err := store.NewFSStore(data)
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
	var fenceCalls, fenceState atomic.Int32
	journal, stranded := strings.Repeat("d", 32), strings.Repeat("e", 32)
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
			w.Header().Set("X-Dill-Journal-Id", journal)
			if enabled.Load() {
				w.Header().Set("X-Dill-Create-Enabled", "1")
			}
			_, _ = w.Write(plan)
		case "/v1/deploy/apply", "/v1/deploy/journal-apply":
			calls.Add(1)
			var f map[string]string
			if json.NewDecoder(r.Body).Decode(&f) != nil || f["stageId"] != stage || f["confirm"] != "create" || f["stack"] != "synthetic" {
				t.Error("invalid create request")
			}
			if calls.Load() == 2 {
				persisted, err := store.NewFSStore(data)
				if err != nil {
					t.Error(err)
					return
				}
				s, err := persisted.Get("synthetic")
				if err != nil || s.RemoteGit == nil || s.RemoteGit.Request != f["requestId"] || s.RemoteGit.Phase != "pending" || s.RemoteGit.Journal != journal || r.URL.Path != "/v1/deploy/journal-apply" || r.Header.Get("X-Dill-Journal-Id") != journal {
					t.Error("Git apply sent before durable intent")
					return
				}
			}
			operationID.Store(f["requestId"])
			// Simulate an ambiguous response; the durable status is discoverable separately.
			w.WriteHeader(502)
		case "/v1/deploy/fence":
			fenceCalls.Add(1)
			var f map[string]string
			if json.NewDecoder(r.Body).Decode(&f) != nil || f["requestId"] != stranded || f["stack"] != "synthetic" || f["confirm"] != "fence-if-unadmitted" || r.Header.Get("X-Dill-Journal-Id") != journal {
				t.Error("unbound fence request")
				w.WriteHeader(409)
				return
			}
			w.Header().Set("X-Dill-Journal-Id", journal)
			if fenceState.Load() == 1 {
				fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"unknown","effects":"unknown"}`, stranded)
				return
			}
			if fenceState.Load() == 2 {
				w.Header().Set("X-Dill-Journal-Id", strings.Repeat("c", 32))
			}
			if fenceState.Load() == 3 {
				w.Header().Set("X-Dill-Journal-Capacity", "low")
				w.WriteHeader(http.StatusInsufficientStorage)
				return
			}
			fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"not-admitted","effects":"none"}`, stranded)
		case "/v1/deploy/operations/" + stranded:
			http.NotFound(w, r)
		default:
			if r.Header.Get("X-Dill-Journal-Id") != "" {
				w.Header().Set("X-Dill-Journal-Id", journal)
			}
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
	t.Run("git persists and reconciles the same request after restart", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git unavailable")
		}
		ctx := context.Background()
		if err := fs.Delete("synthetic"); err != nil {
			t.Fatal(err)
		}
		upstream := t.TempDir()
		run := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = upstream
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %s: %v: %s", args[0], err, out)
			}
			return strings.TrimSpace(string(out))
		}
		run("init", "-b", "deploy")
		run("config", "user.name", "Test")
		run("config", "user.email", "test@example.invalid")
		if err := os.WriteFile(filepath.Join(upstream, "compose.pkl"), []byte("original-reviewed-project"), 0600); err != nil {
			t.Fatal(err)
		}
		run("add", "compose.pkl")
		run("commit", "-m", "approved source")
		commit := run("rev-parse", "HEAD")
		work, err := fs.RepoDir("synthetic")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(work), 0700); err != nil {
			t.Fatal(err)
		}
		g := git.New()
		if err := g.Clone(ctx, upstream, "deploy", work); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.CreateFromGit("synthetic", &store.GitSource{URL: upstream, Branch: "deploy"}, "compose.pkl"); err != nil {
			t.Fatal(err)
		}
		h.EnableGit(g)
		params := url.Values{"_csrf": {csrf}, "stack": {"synthetic"}, "host": {"demo"}}
		w := invoke("POST", deployPath+"/git", params)
		params.Set("source", field(w, "source"))
		params.Set("target", field(w, "target"))
		params.Set("mode", "enable")
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 409 {
			t.Fatal("Git enabled without trust confirmation", w.Code)
		}
		params.Set("confirm", "trusted")
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 200 {
			t.Fatal("Git enabling failed", w.Code)
		}
		if err := os.WriteFile(filepath.Join(work, "compose.pkl"), []byte("mutable checkout must not ship"), 0600); err != nil {
			t.Fatal(err)
		}
		// Keep exact deduplication after crossing the former 128-attempt limit.
		prior, err := fs.Get("synthetic")
		if err != nil {
			t.Fatal(err)
		}
		seed := *prior.RemoteGit
		var earlier []string
		for i := 1; i <= 128; i++ {
			earlier = append(earlier, fmt.Sprintf("%040x", i))
		}
		seed.Seen, seed.Commit = strings.Join(earlier, " "), earlier[len(earlier)-1]
		if err := fs.SwapRemoteGit("synthetic", seed.Source, prior.RemoteGit, &seed); err != nil {
			t.Fatal(err)
		}
		h.pollGit(ctx)
		got, err := fs.Get("synthetic")
		if err != nil || got.RemoteGit == nil || got.RemoteGit.Commit != commit || got.RemoteGit.Request == "" || calls.Load() != 2 {
			t.Fatal("lost response was not retained", err)
		}
		fs2, err := store.NewFSStore(data)
		if err != nil {
			t.Fatal(err)
		}
		restarted, err := NewDeployment(a, reg, fs2)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		restarted.EnableGit(g)
		restarted.pollGit(ctx)
		restarted.pollGit(ctx)
		got, err = fs2.Get("synthetic")
		if err != nil || got.RemoteGit.Phase != "succeeded" || got.RemoteGit.Request != "" || got.RemoteGit.Commit != commit || calls.Load() != 2 {
			t.Fatal("request not recovered or unexpectedly resubmitted", err)
		}
		// Model a crash after the local intent sync but before transmission.
		pending := *got.RemoteGit
		pending.Request, pending.Journal, pending.Phase, pending.Reason = stranded, journal, "pending", "awaiting-status"
		if err := fs2.SwapRemoteGit("synthetic", pending.Source, got.RemoteGit, &pending); err != nil {
			t.Fatal(err)
		}
		restarted.pollGit(ctx)
		readPolicy := func() *store.RemoteGitPolicy {
			t.Helper()
			s, err := fs2.Get("synthetic")
			if err != nil {
				t.Fatal(err)
			}
			return s.RemoteGit
		}
		if p := readPolicy(); p.Request != stranded || p.Reason != "status-unavailable" || fenceCalls.Load() != 0 {
			t.Fatal("404 cleared the pending request")
		}
		handler = restarted.Protected()
		params.Set("mode", "recover")
		params.Set("request", stranded)
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 409 || fenceCalls.Load() != 0 {
			t.Fatal("fence occurred without confirmation", w.Code)
		}
		params.Set("confirm", "fence-unadmitted")
		fenceState.Store(1)
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 200 || readPolicy().Request != stranded || !strings.Contains(w.Body.String(), "already admitted") {
			t.Fatal("unknown admission was cleared", w.Code)
		}
		fenceState.Store(2)
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 200 || readPolicy().Request != stranded || !strings.Contains(w.Body.String(), "No fence was verified") {
			t.Fatal("foreign journal proof was accepted", w.Code)
		}
		fenceState.Store(3)
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 200 || readPolicy().Request != stranded || !strings.Contains(w.Body.String(), "journal is full") {
			t.Fatal("full journal cleared pending intent or showed no warning", w.Code)
		}
		fenceState.Store(0)
		if w := invoke("POST", deployPath+"/git-config", params); w.Code != 200 || !strings.Contains(w.Body.String(), "without admitting it") {
			t.Fatal("fence was not verified", w.Code)
		}
		if p := readPolicy(); p.Enabled || p.Request != "" || p.Journal != "" || p.Reason != "not-admitted" || p.Seen != seed.Seen+" "+commit {
			t.Fatalf("incorrect recovery state: %+v", p)
		}
		restarted.pollGit(ctx)
		if calls.Load() != 2 || fenceCalls.Load() != 4 {
			t.Fatal("recovery caused an automatic submission")
		}
	})
}
