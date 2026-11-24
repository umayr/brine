package remoteui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

const attendedSource = "synthetic-exact-reviewed-file"

var attendedStage = strings.Repeat("a", 32)
var attendedJournal = strings.Repeat("b", 32)
var attendedSelection = "sha256:" + strings.Repeat("c", 64)

type attendedStackFixture struct {
	store.StackStore
	block string
}

func (s *attendedStackFixture) Get(name string) (*store.Stack, error) {
	stack, err := s.StackStore.Get(name)
	if err != nil || s.block == "" {
		return stack, err
	}
	switch s.block {
	case "read error":
		return nil, errors.New("synthetic error")
	case "missing stack":
		return nil, nil
	case "enabled git":
		stack.RemoteGit = &store.RemoteGitPolicy{Enabled: true}
	case "pending git":
		stack.RemoteGit = &store.RemoteGitPolicy{Request: "synthetic"}
	}
	return stack, nil
}

type attendedArchiveFixture struct {
	commits, markers, receipts int
	retained                   []snapshothistory.CommitRequest
	err, markerErr, receiptErr error
	historical                 snapshothistory.Record
	evidence                   snapshothistory.Evidence
	historicalReads            int
}

func (a *attendedArchiveFixture) Commit(_ context.Context, r snapshothistory.CommitRequest) (snapshothistory.OpenRequest, error) {
	a.commits++
	// Model the archive's already-tested post-rename error: ciphertext may
	// already exist even though Commit did not return durable success.
	if a.err == nil || errors.Is(a.err, snapshothistory.ErrIndeterminate) {
		a.retained = append(a.retained, r)
	}
	return snapshothistory.OpenRequest{ObjectID: r.ObjectID, Intent: r.Intent, ManifestDigest: r.ExpectedManifestDigest}, a.err
}

func (a *attendedArchiveFixture) MarkDeliveryUnknown(_ context.Context, _ snapshothistory.OpenRequest, _ time.Time) error {
	a.markers++
	return a.markerErr
}

func (a *attendedArchiveFixture) RecordTerminalObservation(_ context.Context, _ snapshothistory.OpenRequest, _ snapshothistory.TerminalObservation) error {
	a.receipts++
	return a.receiptErr
}

func (a *attendedArchiveFixture) Open(context.Context, snapshothistory.OpenRequest) (snapshothistory.Record, error) {
	a.historicalReads++
	return a.historical, nil
}
func (a *attendedArchiveFixture) ReadEvidence(context.Context, snapshothistory.OpenRequest) (snapshothistory.Evidence, error) {
	return a.evidence, nil
}

type attendedProviderFixture struct {
	t                         *testing.T
	archive                   *attendedArchiveFixture
	applies, stages, previews int
	mode, journal             string
	loss                      bool
	catalog                   dillserve.Manifest
}

func (f *attendedProviderFixture) Stage(ctx context.Context, epoch string, body []byte) (string, error) {
	f.stages++
	var upload struct {
		Entrypoint string
		Files      []struct {
			Path   string
			Bytes  []byte
			Length int
			SHA256 string
		}
	}
	if err := json.Unmarshal(body, &upload); err != nil || len(upload.Files) != 1 || string(upload.Files[0].Bytes) != attendedSource || epoch != "fixture-epoch" {
		f.t.Fatal("stage did not receive exact reviewed source")
	}
	file := upload.Files[0]
	data := fmt.Sprintf(`{"schema":"dill.snapshot.manifest.v1","entrypoint":%q,"files":[{"path":%q,"length":%d,"sha256":%q}]}`, upload.Entrypoint, file.Path, file.Length, file.SHA256)
	catalog := fmt.Sprintf(`{"stage_id":%q,"manifest":%s,"manifest_sha256":"%x"}`, attendedStage, data, sha256.Sum256([]byte(data)))
	var err error
	f.catalog, err = dillserve.ParseCatalog(ctx, attendedStage, []byte(catalog))
	return attendedStage, err
}

func (f *attendedProviderFixture) Snapshot(context.Context, string, string) (dillserve.Manifest, error) {
	return f.catalog, nil
}

func (f *attendedProviderFixture) PreviewDeployment(_ context.Context, _, _, _, stack string) (*agentclient.DeploymentPreview, error) {
	f.previews++
	presence := "absent"
	if f.mode == "update" {
		presence = "present"
	}
	selection := attendedSelection
	return &agentclient.DeploymentPreview{PreflightResult: &dill.PreflightResult{Stack: stack, SelectionDigest: &selection, Resources: []dill.PreflightResource{{Presence: presence}}}, Mode: f.mode, JournalID: f.journal}, nil
}

func (f *attendedProviderFixture) ApplyJournalDeployment(_ context.Context, epoch, stage, manifest, stack, selection, request, mode, journal string) (agentclient.DeploymentStatus, error) {
	f.applies++
	if len(f.archive.retained) != 1 || f.archive.err != nil || f.archive.markers != 1 || f.archive.markerErr != nil || f.archive.receipts != 0 {
		f.t.Fatal("provider called without durable intent and pre-send marker, or after receipt")
	}
	r := f.archive.retained[0]
	a := r.Intent.Attended
	if a == nil || epoch != "fixture-epoch" || stage != a.Stage || manifest != fmt.Sprintf("%x", r.ExpectedManifestDigest) || stack != a.Stack || selection != a.Selection || mode != a.Mode || journal != string(r.Intent.OriginalJournal) || request != string(r.Intent.ProviderRequestID) || request == string(r.Intent.AttemptID) {
		f.t.Fatal("provider inputs did not match archived reviewed binding")
	}
	if f.loss {
		return agentclient.DeploymentStatus{}, agentclient.ErrDeploymentUnknown
	}
	return agentclient.DeploymentStatus{RequestID: request, Stack: stack, State: "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("e", 32)}, nil
}

func (*attendedProviderFixture) Close() error { return nil }

func TestUnmountedAttendedHistoryHandler(t *testing.T) {
	for _, tc := range []struct {
		name, mode, problem, expect string
		commits, applies, retained  int
	}{
		{"create", "create", "", "Verified terminal receipt saved", 1, 1, 1},
		{"update", "update", "", "Verified terminal receipt saved", 1, 1, 1},
		{"no distinct proof", "create", "no proof", "Attended authority unavailable", 0, 0, 0},
		{"no original journal", "create", "no journal", "Cannot retain an exact attended review", 0, 0, 0},
		{"revoked proof", "create", "rotated", "Attended authority changed", 0, 0, 0},
		{"missing route grant", "create", "missing grant", "Attended authority unavailable", 0, 0, 0},
		{"read error", "create", "read error", "Cannot verify remote Git overlap", 0, 0, 0},
		{"missing stack", "create", "missing stack", "Cannot verify remote Git overlap", 0, 0, 0},
		{"enabled git", "create", "enabled git", "Disable or reconcile remote Git", 0, 0, 0},
		{"pending git", "create", "pending git", "Disable or reconcile remote Git", 0, 0, 0},
		{"capacity", "create", "capacity", "no operation sent", 1, 0, 0},
		{"post-rename indeterminate", "create", "post rename", "Local history retention is uncertain", 1, 0, 1},
		{"marker uncertain", "create", "marker uncertain", "No operation was sent", 1, 0, 1},
		{"receipt full", "create", "receipt full", "Live observation only; the local history cap prevented saving a receipt", 1, 1, 1},
		{"create backend absent at confirm", "create", "backend absent", "Attended authority unavailable", 0, 0, 0},
		{"update backend absent at confirm", "update", "backend absent", "Attended authority unavailable", 0, 0, 0},
		{"lost provider reply", "update", "lost reply", "Operation not confirmed. No retry was sent", 1, 1, 1},
		{"restore as new", "update", "restore", "Verified terminal receipt saved", 1, 1, 1},
		{"restore historical unknown", "update", "restore unknown", "", 0, 0, 0},
		{"restore authorization denied", "update", "restore denied", "", 0, 0, 0},
		{"restore rotation fenced", "update", "restore rotation", "", 0, 0, 0},
		{"restore journal changed", "update", "restore journal", "Cannot retain an exact attended review", 0, 0, 0},
		{"restore gate lost during stage", "update", "restore gate lost", "Archived configuration is not authorized", 0, 0, 0},
		{"restore backend gone at confirm", "update", "restore backend gone", "Attended authority unavailable", 0, 0, 0},
		{"restore revoked after preview", "update", "restore revoked", "Archived configuration is not authorized", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive attendedArchiveFixture
			provider := &attendedProviderFixture{t: t, archive: &archive, mode: tc.mode, journal: attendedJournal}
			fs, err := store.NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			stack, err := fs.Create("synthetic", attendedSource)
			if err != nil {
				t.Fatal(err)
			}
			stacks := &attendedStackFixture{StackStore: fs}
			reg, err := targetregistry.Open(filepath.Join(t.TempDir(), "targets"))
			if err != nil {
				t.Fatal(err)
			}
			priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
			key := priv.Public().(ed25519.PublicKey)
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
			pin, address := strings.Repeat("1", 64), "127.0.0.1:1"
			var legacyCalls atomic.Int32
			const engine, socket = "11111111-1111-4111-8111-111111111111", "unix:///run/docker.sock"
			if tc.problem == "backend absent" {
				// The legacy path would successfully establish its own session and
				// reach the apply endpoint. Assert it does not even open that session.
				legacy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					legacyCalls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/v1/session" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					var q struct{ Nonce string }
					if json.NewDecoder(r.Body).Decode(&q) != nil {
						t.Error("invalid generated legacy challenge")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					proof := agentclient.Proof{AgentID: fmt.Sprintf("%x", sha256.Sum256(key)), AgentPublicKey: base64.RawURLEncoding.EncodeToString(key), EngineID: engine, Generation: 1, EngineKind: "docker", EngineSocket: socket, DaemonID: "synthetic", Epoch: strings.Repeat("A", 43), Nonce: q.Nonce}
					message, err := agentclient.SessionBytes(proof)
					if err != nil {
						t.Error("invalid generated legacy proof")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					proof.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, message))
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"version": "brine.agent.read/1", "requestId": r.Header.Get("X-Request-Id"), "data": proof})
				}))
				defer legacy.Close()
				pin, address = fmt.Sprintf("%x", sha256.Sum256(legacy.Certificate().Raw)), strings.TrimPrefix(legacy.URL, "https://")
			}
			pairing := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":"%x","engineId":%q,"generation":1,"engineKind":"docker","engineSocket":%q,"daemonId":"synthetic"}`, pin, token, key, engine, socket)
			row := targetregistry.Record{ID: "demo", Revision: 1, HTTPSAddress: address, HTTPSPairing: targetregistry.PairingSecret(pairing)}
			if err := reg.AddContext(context.Background(), row, row); err != nil {
				t.Fatal(err)
			}
			a, err := auth.New(auth.Config{User: "admin", Password: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewDeployment(a, reg, stacks)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			if h.history != nil {
				t.Fatal("constructor mounted history")
			}
			binding := attendedBinding{target: targetDigest(row), scope: "synthetic-local-target-scope", contractVersion: "dill/0.9.0", revision: strings.Repeat("d", 64), grants: attendedGrants, distinct: tc.problem != "no proof"}
			h.history = &attendedHistory{archive: &archive, open: func(context.Context, targetregistry.Record) (attendedConnection, error) {
				if tc.problem == "restore gate lost" {
					h.history.restoreGate = nil
				}
				return attendedConnection{client: provider, epoch: "fixture-epoch", binding: binding}, nil
			}}
			if tc.problem == "no journal" {
				provider.journal = ""
			}
			login := httptest.NewRecorder()
			lr := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=synthetic"))
			lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			a.HandleLogin(login, lr)
			cookies := login.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal("missing synthetic session")
			}
			handler := h.Protected()
			invoke := func(method, path string, form url.Values) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookies[0])
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if strings.Contains(w.Body.String(), attendedSource) || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), attendedJournal) {
					t.Fatal("history leaked protected data")
				}
				return w
			}
			field := func(w *httptest.ResponseRecorder, key string) string {
				t.Helper()
				m := regexp.MustCompile(`name="` + key + `" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
				if len(m) != 2 {
					t.Fatalf("missing %s in HTTP %d", key, w.Code)
				}
				return m[1]
			}
			csrf := field(invoke("GET", deployPath+"?host=demo", nil), "_csrf")
			form := url.Values{"_csrf": {csrf}, "host": {"demo"}, "stack": {"synthetic"}}
			isRestore := strings.HasPrefix(tc.problem, "restore")
			var prior snapshothistory.OpenRequest
			var gateDenied bool
			if !isRestore {
				form.Set("id", field(invoke("POST", deployPath+"/review", form), "id"))
			} else {
				content := []byte(attendedSource)
				snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
				digest, err := dillserve.SnapshotManifestDigest(context.Background(), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				prior = snapshothistory.OpenRequest{ObjectID: sha256.Sum256([]byte("prior-object")), ManifestDigest: digest, Intent: snapshothistory.Intent{
					AttemptID: []byte(strings.Repeat("1", 32)), ProviderRequestID: []byte(strings.Repeat("2", 32)), OriginalJournal: []byte(attendedJournal), ApprovedTargetScope: []byte(binding.scope),
					Attended: &snapshothistory.AttendedIntent{Target: binding.target, Stack: "synthetic", AuthorityRevision: binding.revision},
				}}
				archive.historical = snapshothistory.Record{ObjectID: prior.ObjectID, Intent: prior.Intent, ManifestDigest: digest, Snapshot: snapshot}
				archive.evidence = snapshothistory.Evidence{State: snapshothistory.VerifiedReceipt, Receipt: &snapshothistory.TerminalObservation{State: "succeeded", Effects: "complete", ContractVersion: binding.contractVersion}}
				gateDenied = tc.problem == "restore denied"
				h.history.restoreGate = func(_ context.Context, _ string, _ projectreview.Current, object snapshothistory.ObjectID) error {
					if gateDenied || object != prior.ObjectID {
						return errRestoreReview
					}
					return nil
				}
				if tc.problem == "restore unknown" {
					archive.evidence = snapshothistory.Evidence{State: snapshothistory.DeliveryUnknown}
				}
				if tc.problem == "restore rotation" {
					binding.revision = strings.Repeat("f", 64)
				}
				if err := os.WriteFile(stack.PklPath, []byte("current workspace differs from history"), 0600); err != nil {
					t.Fatal(err)
				}
				session := fmt.Sprintf("%x", sha256.Sum256([]byte("brine-remote-deployment\x00"+csrf)))
				current := projectreview.Current{Stack: "synthetic", Target: projectreview.Target{Kind: projectreview.HTTPS, Identity: binding.target, Socket: row.HTTPSAddress, Endpoint: row.ID}}
				preview, err := h.prepareRestoreReview(context.Background(), session, current, binding, prior)
				if tc.problem == "restore unknown" || tc.problem == "restore denied" || tc.problem == "restore rotation" {
					if err == nil || preview.ID != "" || archive.historicalReads != 0 || provider.stages != 0 || provider.applies != 0 {
						t.Fatal("unauthorized or unresolved history reached byte review")
					}
					return
				}
				if err != nil || preview.ID == "" {
					t.Fatalf("fresh review unavailable: %v", err)
				}
				form.Set("id", preview.ID)
				if tc.problem == "restore journal" {
					provider.journal = strings.Repeat("9", 32)
				}
			}
			if err := os.WriteFile(stack.PklPath, []byte("different-after-review"), 0600); err != nil {
				t.Fatal(err)
			}
			form.Set("confirm", "transfer")
			stage := invoke("POST", deployPath+"/stage", form)
			if tc.problem == "no proof" || tc.problem == "no journal" || tc.problem == "restore journal" || tc.problem == "restore gate lost" {
				wantStages := 1
				if tc.problem == "no proof" || tc.problem == "restore gate lost" {
					wantStages = 0
				}
				if !strings.Contains(stage.Body.String(), tc.expect) || archive.commits != 0 || provider.applies != 0 || provider.stages != wantStages || strings.Contains(stage.Body.String(), `action="/remote/deployment/create"`) {
					t.Fatal("stage did not fail closed")
				}
				return
			}
			if provider.previews != 1 || archive.commits != 0 || provider.applies != 0 || strings.Contains(stage.Body.String(), "Open status link") {
				t.Fatal("stage exposed an unbound lookup or sent before confirmation")
			}
			id := field(stage, "id")
			switch tc.problem {
			case "rotated":
				binding.revision = strings.Repeat("e", 64)
			case "missing grant":
				binding.grants &^= attendedJournalApplyGrant
			case "capacity":
				archive.err = snapshothistory.ErrCapacity
			case "post rename":
				archive.err = snapshothistory.ErrIndeterminate
			case "marker uncertain":
				archive.markerErr = snapshothistory.ErrIndeterminate
			case "receipt full":
				archive.receiptErr = snapshothistory.ErrCapacity
			case "lost reply":
				provider.loss = true
			case "backend absent":
				h.history = nil
			case "restore revoked":
				gateDenied = true
			case "restore backend gone":
				h.history = nil
			default:
				stacks.block = tc.problem
			}
			submit := url.Values{"_csrf": {csrf}, "id": {id}, "confirm": {tc.mode}}
			w := invoke("POST", deployPath+"/create", submit)
			if !strings.Contains(w.Body.String(), tc.expect) || strings.Contains(w.Body.String(), "Refresh status") {
				t.Fatalf("unexpected response HTTP %d", w.Code)
			}
			if w := invoke("POST", deployPath+"/create", submit); w.Code != http.StatusConflict {
				t.Fatalf("same confirmation reused: HTTP %d", w.Code)
			}
			if archive.commits != tc.commits || provider.applies != tc.applies || len(archive.retained) != tc.retained {
				t.Fatalf("commit/apply/retained %d/%d/%d", archive.commits, provider.applies, len(archive.retained))
			}
			if legacyCalls.Load() != 0 {
				t.Fatalf("retained attended confirmation contacted legacy provider %d times", legacyCalls.Load())
			}
			wantMarkers, wantReceipts := tc.applies, tc.applies
			if tc.problem == "marker uncertain" {
				wantMarkers = 1
			}
			if tc.problem == "lost reply" {
				wantReceipts = 0
			}
			if archive.markers != wantMarkers || archive.receipts != wantReceipts {
				t.Fatalf("marker/receipt calls %d/%d", archive.markers, archive.receipts)
			}
			if tc.problem == "receipt full" && (!strings.Contains(w.Body.String(), "State succeeded") || strings.Contains(w.Body.String(), "Verified terminal receipt saved")) {
				t.Fatal("live result was hidden or was misrepresented as a saved receipt")
			}
			if len(archive.retained) > 0 {
				r := archive.retained[0]
				if isRestore && (r.ObjectID == prior.ObjectID || r.Intent.Attended == nil || r.Intent.Attended.PriorObject != fmt.Sprintf("%x", prior.ObjectID) ||
					string(r.Intent.AttemptID) == string(prior.Intent.AttemptID) || string(r.Intent.ProviderRequestID) == string(prior.Intent.ProviderRequestID) || archive.evidence.State != snapshothistory.VerifiedReceipt) {
					t.Fatal("new operation reused or modified historical identity/evidence")
				}
				if r.Intent.Attended == nil || r.Intent.Attended.Target != targetDigest(row) || string(r.Intent.ApprovedTargetScope) != binding.scope || string(r.Intent.AttemptID) != id || string(r.Intent.OriginalJournal) != attendedJournal || string(r.Snapshot.Files[0].Content) != attendedSource {
					t.Fatal("archive lost reviewed binding or exact bytes")
				}
			}
		})
	}
}

func TestAttendedReviewRetentionBudgetAndExpiry(t *testing.T) {
	h := &Deployment{pending: make(map[string]deploymentReview)}
	id := h.add(deploymentReview{Session: "cookie", Mode: "update", Expiry: time.Now().Add(50 * time.Millisecond), Attended: &attendedReview{bytes: maxAttendedReviewBytes}})
	if id == "" {
		t.Fatal("max legal review refused")
	}
	if h.add(deploymentReview{Expiry: time.Now().Add(time.Minute), Attended: &attendedReview{bytes: maxAttendedReviewBytes + 1}}) != "" {
		t.Fatal("over-budget review retained")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		cleared := h.attendedBytes == 0 && len(h.pending) == 0
		h.mu.Unlock()
		if cleared {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expired review still retained without traffic")
}
