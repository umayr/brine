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
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/git"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestPartialFailureAlertOnlyForPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		state, effects string
		alert          bool
	}{
		{"failed", "partial", true}, {"succeeded", "complete", false},
		{"running", "unknown", false}, {"failed", "none", false}, {"unknown", "unknown", false},
	} {
		t.Run(tc.state+"-"+tc.effects, func(t *testing.T) {
			var body bytes.Buffer
			err := deploymentView.Execute(&body, deploymentPage{Status: &agentclient.DeploymentStatus{State: tc.state, Effects: tc.effects}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body.String(), "The operation failed after partially changing the target.") != tc.alert {
				t.Fatalf("unexpected partial alert: %s", body.String())
			}
		})
	}
}

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
	var previewResult struct {
		Selection string `json:"selectionDigest"`
	}
	if json.Unmarshal(plan, &previewResult) != nil || previewResult.Selection == "" {
		t.Fatal("fixture missing reviewed selection")
	}
	removeFixture, err := os.ReadFile("../dill/testdata/preflight-v1/preview-down-absent.json")
	if err != nil || json.Unmarshal(removeFixture, &golden) != nil {
		t.Fatal("invalid synthetic removal preview fixture")
	}
	removePlan := bytes.ReplaceAll(golden.Stdout, []byte("sha256:"+strings.Repeat("1", 64)), []byte(fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(socket)))))
	var removeSelection struct {
		Selection string `json:"selectionDigest"`
	}
	if json.Unmarshal(removePlan, &removeSelection) != nil || removeSelection.Selection == "" {
		t.Fatal("removal fixture missing selection digest")
	}
	prior := strings.Repeat("c", 32)
	var removePreviews, removeCalls, removeGETs atomic.Int32
	var removalRequest atomic.Value
	var calls atomic.Int32
	var manifest, catalogDigest atomic.Value
	var differentManifest atomic.Bool
	var catalogReads, previewCalls atomic.Int32
	var operationID atomic.Value
	var fenceCalls, fenceState atomic.Int32
	var partial atomic.Bool
	var statusGETs atomic.Int32
	journal, stranded, unknown := strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32)
	unbound := strings.Repeat("9", 32)
	var unboundGETs atomic.Int32
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
				observed := file{Path: row.Path, Length: len(row.Bytes), SHA256: fmt.Sprintf("%x", sha256.Sum256(row.Bytes))}
				if row.file != observed {
					t.Error("uploaded metadata did not match the uploaded bytes")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if row.Path == req.Entrypoint {
					found = string(row.Bytes) == "original-reviewed-project"
					if differentManifest.Load() {
						// Use equally sized alternate fixture bytes: the catalog
						// differs in exactly one valid field, the file digest.
						alternate := bytes.Repeat([]byte{'x'}, observed.Length)
						observed.SHA256 = fmt.Sprintf("%x", sha256.Sum256(alternate))
					}
				}
				files = append(files, observed)
			}
			if !found {
				t.Error("uploaded bytes were not the reviewed capture")
			}
			raw, _ := json.Marshal(struct {
				Schema     string `json:"schema"`
				Entrypoint string `json:"entrypoint"`
				Files      []file `json:"files"`
			}{"dill.snapshot.manifest.v1", req.Entrypoint, files})
			digest := fmt.Sprintf("%x", sha256.Sum256(raw))
			catalog := fmt.Sprintf(`{"stage_id":%q,"manifest":%s,"manifest_sha256":%q}`, stage, raw, digest)
			if differentManifest.Load() {
				if _, err := dillserve.ParseCatalog(r.Context(), stage, []byte(catalog)); err != nil {
					t.Errorf("mismatched fixture must still be a valid catalog: %v", err)
				}
			}
			manifest.Store(catalog)
			catalogDigest.Store(digest)
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"status":"staged","stage_id":%q}`, stage)
		case "/v1/snapshots/" + stage:
			catalogReads.Add(1)
			fmt.Fprint(w, manifest.Load().(string))
		case "/v1/deploy/preview":
			previewCalls.Add(1)
			var f map[string]string
			if json.NewDecoder(r.Body).Decode(&f) != nil || f["stageId"] != stage || f["stack"] != "synthetic" || f["manifestSha256"] != catalogDigest.Load() {
				t.Error("preview not bound to staged catalog")
			}
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
			if !partial.Load() && (f["manifestSha256"] != catalogDigest.Load() || f["selectionDigest"] != previewResult.Selection) {
				t.Error("apply not bound to staged catalog and reviewed selection")
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
			if partial.Load() {
				fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"failed","effects":"partial","operationId":%q}`, f["requestId"], "op_"+f["requestId"])
				return
			}
			// Simulate an ambiguous response; the durable status is discoverable separately.
			w.WriteHeader(502)
		case "/v1/deploy/remove-preview":
			removePreviews.Add(1)
			var f map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&f) != nil || len(f) != 1 || f["stack"] != "synthetic" {
				t.Error("unexpected remove preview request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("X-Dill-Deployment-Mode", "remove")
			w.Header().Set("X-Dill-Prior-Request", prior)
			_, _ = w.Write(removePlan)
		case "/v1/deploy/remove":
			removeCalls.Add(1)
			var f map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&f) != nil || len(f) != 5 ||
				f["stack"] != "synthetic" || f["selectionDigest"] != removeSelection.Selection ||
				f["priorRequestId"] != prior || f["confirm"] != "remove" || f["requestId"] == "" {
				t.Error("remove did not preserve the preview binding")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			removalRequest.Store(f["requestId"])
			// An ambiguous 502: only this synthetic provider records seeing the request.
			w.WriteHeader(http.StatusBadGateway)
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
		case "/v1/deploy/operations/" + unknown:
			fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"unknown","effects":"unknown"}`, unknown)
		case "/v1/deploy/operations/" + unbound:
			if r.Method != http.MethodGet || r.Header.Get("X-Dill-Journal-Id") != "" || r.ContentLength != 0 {
				t.Error("generic status must be a bodyless, unbound GET")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			unboundGETs.Add(1)
			// Even a reply header cannot bind a request to its original journal.
			w.Header().Set("X-Dill-Journal-Id", journal)
			fmt.Fprintf(w, `{"requestId":%q,"stack":"unverified-stack","state":"not-admitted","effects":"none"}`, unbound)
		default:
			if id, _ := removalRequest.Load().(string); id != "" && r.URL.Path == "/v1/deploy/operations/"+id {
				if r.Method != http.MethodGet || r.ContentLength != 0 || r.Header.Get("X-Dill-Journal-Id") != "" {
					t.Error("expected a bodyless generic status GET after lost response")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				removeGETs.Add(1)
				fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"unknown","effects":"unknown"}`, id)
				return
			}
			if r.Header.Get("X-Dill-Journal-Id") != "" {
				w.Header().Set("X-Dill-Journal-Id", journal)
			}
			id, _ := operationID.Load().(string)
			if id == "" || r.URL.Path != "/v1/deploy/operations/"+id {
				t.Error("unexpected route")
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet {
				t.Errorf("status lookup used %s", r.Method)
			}
			statusGETs.Add(1)
			if partial.Load() {
				fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"failed","effects":"partial","operationId":%q}`, id, "op_"+id)
			} else {
				fmt.Fprintf(w, `{"requestId":%q,"stack":"synthetic","state":"succeeded","effects":"complete","operationId":%q}`, id, "op_"+id)
			}
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
	session := fmt.Sprintf("%x", sha256.Sum256([]byte("brine-remote-deployment\x00"+csrf)))
	for _, mode := range []string{"create", "update", "remove"} {
		for _, tc := range []struct {
			name  string
			err   error
			stack *store.Stack
		}{
			{"unreadable", fmt.Errorf("metadata unreadable"), nil},
			{"malformed", fmt.Errorf("metadata malformed"), nil},
			{"nil", nil, nil},
			{"wrong-name", nil, &store.Stack{Name: "other"}},
			{"missing", fmt.Errorf("lookup: %w", store.ErrStackNotFound), nil},
		} {
			if mode == "remove" && tc.name == "missing" {
				continue
			} // existing remote-only removal remains supported
			t.Run("overlap-"+mode+"-"+tc.name, func(t *testing.T) {
				original := h.stacks
				h.stacks = overlapErrorStore{StackStore: original, err: tc.err, stack: tc.stack}
				defer func() { h.stacks = original }()
				beforeApply, beforeRemove := calls.Load(), removeCalls.Load()
				id := h.add(deploymentReview{Session: session, Host: "demo", Stack: "synthetic", Mode: mode, TargetDigest: targetDigest(row), Expiry: time.Now().Add(time.Minute)})
				w := invoke("POST", deployPath+"/create", url.Values{"_csrf": {csrf}, "id": {id}, "confirm": {mode}})
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Cannot verify remote Git overlap") {
					t.Fatalf("expected fail-closed overlap rejection: %d %s", w.Code, w.Body.String())
				}
				if calls.Load() != beforeApply || removeCalls.Load() != beforeRemove {
					t.Fatal("unreadable overlap state reached mutation")
				}
			})
		}
	}
	t.Run("missing-metadata-blocks-removal", func(t *testing.T) {
		path := filepath.Join(data, "stacks", "synthetic", "meta.json")
		metadata, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(path, metadata, 0600); err != nil {
				t.Fatal(err)
			}
		}()
		id := h.add(deploymentReview{Session: session, Host: "demo", Stack: "synthetic", Mode: "remove", TargetDigest: targetDigest(row), Expiry: time.Now().Add(time.Minute)})
		before := removeCalls.Load()
		w := invoke("POST", deployPath+"/create", url.Values{"_csrf": {csrf}, "id": {id}, "confirm": {"remove"}})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Cannot verify remote Git overlap") || removeCalls.Load() != before {
			t.Fatalf("missing metadata removal: %d %s", w.Code, w.Body.String())
		}
	})
	for _, tc := range []struct {
		name, target, message string
		expiry                time.Time
	}{
		{"expired", targetDigest(row), "Missing, expired, or already used confirmation", time.Now().Add(-time.Minute)},
		{"changed target", "different-reviewed-target", "Target was re-paired; no creation sent", time.Now().Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := h.add(deploymentReview{Session: session, Host: "demo", Stack: "synthetic", Mode: "create", TargetDigest: tc.target, Expiry: tc.expiry})
			if id == "" {
				t.Fatal("could not create confirmation fixture")
			}
			w := invoke("POST", deployPath+"/create", url.Values{"_csrf": {csrf}, "id": {id}, "confirm": {"create"}})
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), tc.message) {
				t.Fatalf("expected reviewed confirmation to be rejected: HTTP %d", w.Code)
			}
			if calls.Load() != 0 {
				t.Fatal("rejected confirmation reached apply")
			}
		})
	}
	// Exercise the immediate result and a later GET against the same validated status.
	partial.Store(true)
	partialID := h.add(deploymentReview{Session: session, Host: "demo", Stack: "synthetic", StageID: stage, Manifest: strings.Repeat("a", 64), Selection: "sha256:" + strings.Repeat("b", 64), Mode: "create", TargetDigest: targetDigest(row), Expiry: time.Now().Add(time.Minute)})
	partialPath := deployPath + "?host=demo&id=" + partialID
	for _, w := range []*httptest.ResponseRecorder{
		invoke("POST", deployPath+"/create", url.Values{"_csrf": {csrf}, "id": {partialID}, "confirm": {"create"}}),
		invoke("GET", partialPath, nil),
	} {
		for _, want := range []string{`role="alert">The operation failed after partially changing the target.`, "Inspect the target and the recorded Dill operation", "do not retry", `value="` + partialID + `"`, `op_` + partialID, "Target: <bdi>demo</bdi>", `href="` + deployPath + `?host=demo&amp;id=` + partialID + `">Refresh status`, "Stack synthetic · State failed · Effects partial"} {
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Errorf("partial status missing %q (HTTP %d)", want, w.Code)
			}
		}
	}
	if calls.Load() != 1 || statusGETs.Load() != 1 || fenceCalls.Load() != 0 {
		t.Fatal("partial status lookup resubmitted or did not use GET")
	}
	partial.Store(false)
	calls.Store(0)
	params := url.Values{"_csrf": {csrf}, "host": {"demo"}, "stack": {"synthetic"}}
	t.Run("catalog must match reviewed bytes before preview", func(t *testing.T) {
		differentManifest.Store(true)
		defer differentManifest.Store(false)
		w := invoke("POST", deployPath+"/review", params)
		stageParams := url.Values{"_csrf": {csrf}, "host": {"demo"}, "stack": {"synthetic"}, "id": {field(w, "id")}, "confirm": {"transfer"}}
		w = invoke("POST", deployPath+"/stage", stageParams)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Staging or preview could not be confirmed") || strings.Contains(w.Body.String(), `action="/remote/deployment/create"`) {
			t.Fatalf("changed manifest did not suppress confirmation: HTTP %d", w.Code)
		}
		if catalogReads.Load() != 1 || previewCalls.Load() != 0 || calls.Load() != 0 {
			t.Fatal("expected one catalog read and no preview or apply")
		}
	})
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
	if previewCalls.Load() != 1 || calls.Load() != 0 || !strings.Contains(w.Body.String(), "Create stack") {
		t.Fatal("stage unexpectedly applied")
	}
	params = url.Values{"id": {id}, "_csrf": {"wrong"}, "confirm": {"create"}}
	if w := invoke("POST", deployPath+"/create", params); w.Code == 200 || calls.Load() != 0 {
		t.Fatal("invalid CSRF admitted")
	}
	params.Set("_csrf", csrf)
	statusPath := deployPath + "?host=demo&id=" + id
	assertStatusControls := func(w *httptest.ResponseRecorder, requestID, host string) {
		t.Helper()
		body := w.Body.String()
		for _, want := range []string{
			`<label for="deployment-request-id">Request ID</label>`,
			`id="deployment-request-id" value="` + requestID + `" readonly`,
			`href="` + deployPath + `?host=` + host + `&amp;id=` + requestID + `">Refresh status</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("status view missing %q", want)
			}
		}
	}
	if w := invoke("POST", deployPath+"/create", params); !strings.Contains(w.Body.String(), "No retry was sent") || calls.Load() != 1 {
		t.Fatal("lost response not held", w.Code)
	} else {
		assertStatusControls(w, id, "demo")
	}
	if w := invoke("POST", deployPath+"/create", params); w.Code != 409 || calls.Load() != 1 {
		t.Fatal("create replayed", w.Code)
	}
	if w := invoke("GET", statusPath, nil); !strings.Contains(w.Body.String(), "State succeeded") || calls.Load() != 1 {
		t.Fatal("status did not reconcile request", w.Code)
	} else {
		assertStatusControls(w, id, "demo")
	}
	if w := invoke("GET", deployPath+"?host=demo&id="+unknown, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Outcome unknown. Dill recorded this request") || strings.Contains(w.Body.String(), "Status unavailable") {
		t.Fatal("recorded unknown was not distinguished from unavailable status", w.Code)
	} else {
		assertStatusControls(w, unknown, "demo")
	}
	w = invoke("GET", deployPath+"?host=demo&id="+unbound, nil)
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, `role="alert">Unverified status reply.`) ||
		!strings.Contains(body, "do not retry based on this result") || strings.Contains(body, "State not-admitted") ||
		strings.Contains(body, "Effects none") || strings.Contains(body, "unverified-stack") {
		t.Errorf("generic status was not rendered as unverified: HTTP %d", w.Code)
	}
	assertStatusControls(w, unbound, "demo")
	if unboundGETs.Load() != 1 || calls.Load() != 1 || fenceCalls.Load() != 0 {
		t.Fatal("unverified generic status did not use GET or sent a mutation")
	}
	for _, tc := range []struct {
		host, id string
		code     int
	}{{"demo", stranded, http.StatusOK}, {"missing", id, http.StatusConflict}} {
		w := invoke("GET", deployPath+"?host="+tc.host+"&id="+tc.id, nil)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), "Status unavailable") || !strings.Contains(w.Body.String(), "Check the original target and its journal, and inspect the target before taking further action; do not retry based on this result.") || calls.Load() != 1 || fenceCalls.Load() != 0 {
			t.Errorf("unavailable status for %s: HTTP %d", tc.host, w.Code)
		}
		assertStatusControls(w, tc.id, tc.host)
	}
	t.Run("remove review binds prior request and permits only one submission", func(t *testing.T) {
		w := invoke(http.MethodPost, deployPath+"/remove-preview", url.Values{"_csrf": {csrf}, "host": {"demo"}, "stack": {"synthetic"}})
		id := field(w, "id")
		for _, want := range []string{`name="confirm" value="remove" required`, "Remove containers; keep data", "Save the status link before confirming"} {
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
				t.Fatalf("removal preview missing %q: HTTP %d", want, w.Code)
			}
		}
		if strings.Contains(w.Body.String(), "Create stack") || removePreviews.Load() != 1 || removeCalls.Load() != 0 {
			t.Fatal("preview sent a mutation or offered the wrong action")
		}
		confirm := url.Values{"_csrf": {csrf}, "id": {id}, "confirm": {"create"}}
		if w := invoke(http.MethodPost, deployPath+"/create", confirm); w.Code != http.StatusConflict || removeCalls.Load() != 0 {
			t.Fatalf("wrong confirmation admitted: HTTP %d", w.Code)
		}
		confirm.Set("confirm", "remove")
		confirm.Set("priorRequestId", "not-the-reviewed-prior-request")
		w = invoke(http.MethodPost, deployPath+"/create", confirm)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "No retry was sent") || removeCalls.Load() != 1 || removalRequest.Load() != id {
			t.Fatalf("ambiguous removal result not tied to the original request: HTTP %d", w.Code)
		}
		assertStatusControls(w, id, "demo")
		if w := invoke(http.MethodPost, deployPath+"/create", confirm); w.Code != http.StatusConflict || removeCalls.Load() != 1 {
			t.Fatalf("repeat removal confirmation was not rejected: HTTP %d", w.Code)
		}
		w = invoke(http.MethodGet, deployPath+"?host=demo&id="+id, nil)
		assertStatusControls(w, id, "demo")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Outcome unknown") || removeGETs.Load() != 1 ||
			removeCalls.Load() != 1 || calls.Load() != 1 || fenceCalls.Load() != 0 || strings.Contains(w.Body.String(), `<form`) {
			t.Fatalf("removal lookup was not read-only and uncertain: HTTP %d", w.Code)
		}
	})
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
