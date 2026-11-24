//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/targetregistry"
)

type competingArchiveProbe struct {
	realAttendedArchiveProbe
	commitErr error
}

func (p *competingArchiveProbe) Commit(ctx context.Context, r snapshothistory.CommitRequest) (snapshothistory.OpenRequest, error) {
	ref, err := p.realAttendedArchiveProbe.Commit(ctx, r)
	p.commitErr = err
	return ref, err
}

type competingWireClient struct{ *agentclient.Client }

func TestCompetingAttendedConfirmationsShareStoreAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := realAttendedArchiveHome(t)
	for _, name := range []string{"archive", "key", "workspace"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(root, "key", "generated")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	options := snapshothistory.Options{ArchiveDir: filepath.Join(root, "archive"), KeyFile: key, WorkspaceDir: filepath.Join(root, "workspace"), StoreID: sha256.Sum256([]byte("competing-store"))}
	if err := snapshothistory.Initialize(ctx, options); err != nil {
		t.Fatal(err)
	}
	archive, err := snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	content := []byte("synthetic competing confirmation")
	snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
	digest, err := dillserve.SnapshotManifestDigest(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("%x", digest)
	const stack = "synthetic"
	firstRequest, secondRequest := strings.Repeat("4", 32), strings.Repeat("5", 32)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var wireCalls atomic.Int32
	row := attendedWireTarget(t, func(w http.ResponseWriter, r *http.Request) {
		if wireCalls.Add(1) != 1 || !validAttendedWireApply(r, firstRequest, manifest) {
			t.Error("unexpected competing or repeated wire apply")
			w.WriteHeader(http.StatusConflict)
			return
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Dill-Journal-Id", attendedJournal)
		_ = json.NewEncoder(w).Encode(agentclient.DeploymentStatus{RequestID: firstRequest, Stack: stack, State: "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("a", 32)})
	})
	reg, err := targetregistry.Open(filepath.Join(root, "targets"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.AddContext(ctx, row, row); err != nil {
		t.Fatal(err)
	}
	_, incarnation, lease, err := reg.AcquireHistoryRead(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	binding := attendedBinding{scope: hex.EncodeToString(incarnation[:]), target: targetDigest(row), revision: strings.Repeat("b", 64), contractVersion: "dill/0.9.0", grants: attendedGrants, distinct: true}
	if err := archive.CheckAttendedIdle(ctx, binding.scope, stack); err != nil {
		t.Fatalf("fixture was not idle: %v", err)
	}
	stacks, err := store.NewFSStore(options.WorkspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stacks.Create(stack, string(content)); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		httpHandler http.Handler
		cookie      *http.Cookie
		probe       *competingArchiveProbe
		csrf, id    string
	}
	makeConfirmation := func(requestID string) fixture {
		t.Helper()
		probe := &competingArchiveProbe{realAttendedArchiveProbe: realAttendedArchiveProbe{Store: archive}}
		h, err := NewDeployment(a, reg, stacks)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(h.Close)
		h.history = &attendedHistory{archive: probe, open: func(ctx context.Context, r targetregistry.Record) (attendedConnection, error) {
			p, err := agentclient.ParsePairing([]byte(r.HTTPSPairing))
			if err != nil {
				return attendedConnection{}, err
			}
			c, epoch, err := p.Open(ctx, ctx, r.HTTPSAddress)
			if err != nil {
				return attendedConnection{}, err
			}
			return attendedConnection{client: competingWireClient{c}, epoch: epoch, binding: binding}, nil
		}}
		login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("user=admin&password=synthetic"))
		login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		loginResult := httptest.NewRecorder()
		a.HandleLogin(loginResult, login)
		cookies := loginResult.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing synthetic session")
		}
		get := httptest.NewRequest(http.MethodGet, deployPath+"?host="+row.ID, nil)
		get.AddCookie(cookies[0])
		page := httptest.NewRecorder()
		handler := h.Protected()
		handler.ServeHTTP(page, get)
		csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
		if len(csrf) != 2 {
			t.Fatal("missing CSRF")
		}
		id := h.add(deploymentReview{Session: deploymentSession(csrf[1]), Host: row.ID, Stack: stack, StageID: attendedStage, Manifest: manifest, Selection: attendedSelection, TargetDigest: binding.target, Mode: "create", Expiry: time.Now().Add(time.Minute), Attended: &attendedReview{snapshot: snapshot, binding: binding, manifest: digest, journal: attendedJournal, requestID: requestID}})
		if id == "" {
			t.Fatal("could not seed separately reviewed confirmation")
		}
		return fixture{httpHandler: handler, cookie: cookies[0], probe: probe, csrf: csrf[1], id: id}
	}
	// Different sessions bypass auth's per-cookie submission lease. Different
	// handler instances bypass the handler-local Git exclusion lock. The only
	// shared admission lock is the real archive; a busy/CSRF reply is not enough.
	first, second := makeConfirmation(firstRequest), makeConfirmation(secondRequest)
	if first.id == second.id || first.csrf == second.csrf || first.cookie.Value == second.cookie.Value {
		t.Fatal("fixtures must be distinct confirmations and sessions")
	}
	var handlers sync.WaitGroup
	defer func() { cancel(); unblock(); handlers.Wait() }()
	submit := func(f fixture) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			form := url.Values{"id": {f.id}, "confirm": {"create"}, "_csrf": {f.csrf}}
			r := httptest.NewRequest(http.MethodPost, deployPath+"/create", strings.NewReader(form.Encode())).WithContext(ctx)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(f.cookie)
			w := httptest.NewRecorder()
			f.httpHandler.ServeHTTP(w, r)
			done <- w
		}()
		return done
	}
	wait := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case w := <-done:
			return w
		case <-ctx.Done():
			t.Fatal("confirmation handler did not terminate")
			return nil
		}
	}
	firstDone := submit(first)
	select {
	case <-entered:
	case w := <-firstDone:
		t.Fatalf("first handler returned before apply: HTTP %d", w.Code)
	case <-ctx.Done():
		t.Fatal("first apply did not reach generated provider")
	}
	blocked := wait(submit(second))
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "An unresolved local operation blocks this deployment") || second.probe.commitCalls != 1 || !errors.Is(second.probe.commitErr, snapshothistory.ErrUnresolvedOperation) || second.probe.markerCalls != 0 || second.probe.receiptCalls != 0 || wireCalls.Load() != 1 {
		t.Fatalf("second confirmation was not rejected by real store: HTTP %d", blocked.Code)
	}
	select {
	case w := <-firstDone:
		t.Fatalf("first handler completed before reply release: HTTP %d", w.Code)
	default:
	}
	unblock()
	done := wait(firstDone)
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), "Verified terminal receipt saved") || first.probe.commitCalls != 1 || first.probe.markerCalls != 1 || first.probe.receiptCalls != 1 || wireCalls.Load() != 1 {
		t.Fatalf("first confirmation did not finish with exactly one apply and receipt: HTTP %d", done.Code)
	}
}
