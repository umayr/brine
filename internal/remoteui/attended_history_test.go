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
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
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
	commits  int
	retained []snapshothistory.CommitRequest
	err      error
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
	if len(f.archive.retained) != 1 || f.archive.err != nil {
		f.t.Fatal("provider called without durable intent")
	}
	r := f.archive.retained[0]
	a := r.Intent.Attended
	if a == nil || epoch != "fixture-epoch" || stage != a.Stage || manifest != fmt.Sprintf("%x", r.ExpectedManifestDigest) || stack != a.Stack || selection != a.Selection || mode != a.Mode || journal != string(r.Intent.OriginalJournal) || request != string(r.Intent.ProviderRequestID) || request == string(r.Intent.AttemptID) {
		f.t.Fatal("provider inputs did not match archived reviewed binding")
	}
	if f.loss {
		return agentclient.DeploymentStatus{}, agentclient.ErrDeploymentUnknown
	}
	return agentclient.DeploymentStatus{RequestID: request, Stack: stack, State: "succeeded", Effects: "complete"}, nil
}

func (*attendedProviderFixture) Close() error { return nil }

func TestUnmountedAttendedHistoryHandler(t *testing.T) {
	for _, tc := range []struct {
		name, mode, problem, expect string
		commits, applies, retained  int
	}{
		{"create", "create", "", "State succeeded", 1, 1, 1},
		{"update", "update", "", "State succeeded", 1, 1, 1},
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
		{"lost provider reply", "update", "lost reply", "Operation not confirmed. No retry was sent", 1, 1, 1},
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
			key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
			token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
			pairing := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":"%x","engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`, strings.Repeat("1", 64), token, key)
			row := targetregistry.Record{ID: "demo", Revision: 1, HTTPSAddress: "127.0.0.1:1", HTTPSPairing: targetregistry.PairingSecret(pairing)}
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
			binding := attendedBinding{target: targetDigest(row), scope: "synthetic-local-target-scope", revision: strings.Repeat("d", 64), grants: attendedGrants, distinct: tc.problem != "no proof"}
			h.history = &attendedHistory{archive: &archive, open: func(context.Context, targetregistry.Record) (attendedConnection, error) {
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
			form.Set("id", field(invoke("POST", deployPath+"/review", form), "id"))
			if err := os.WriteFile(stack.PklPath, []byte("different-after-review"), 0600); err != nil {
				t.Fatal(err)
			}
			form.Set("confirm", "transfer")
			stage := invoke("POST", deployPath+"/stage", form)
			if tc.problem == "no proof" || tc.problem == "no journal" {
				wantStages := 1
				if tc.problem == "no proof" {
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
			case "lost reply":
				provider.loss = true
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
			if len(archive.retained) > 0 {
				r := archive.retained[0]
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
