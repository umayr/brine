//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRestartMetadataRealArchiveReopenAndReenrollment(t *testing.T) {
	root := realAttendedArchiveHome(t)
	ctx := context.Background()
	h, cookie := restartHTTPFixture(t)
	row, id, lease, err := h.registry.AcquireHistoryRead(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	scope := hex.EncodeToString(id[:])
	for _, name := range []string{"archive", "key", "workspace"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(root, "key", "generated")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	options := snapshothistory.Options{ArchiveDir: filepath.Join(root, "archive"), KeyFile: key, WorkspaceDir: filepath.Join(root, "workspace"), StoreID: sha256.Sum256([]byte("metadata-http"))}
	if err := snapshothistory.Initialize(ctx, options); err != nil {
		t.Fatal(err)
	}
	store, err := snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	content := []byte("GENERATED_ARCHIVE_CONTENT_NEVER_DELIVERED")
	snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
	digest, err := dillserve.SnapshotManifestDigest(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	locator := snapshothistory.ObjectID{1}
	for i, s := range []string{scope, "unrelated-enrollment"} {
		_, err = store.Commit(ctx, snapshothistory.CommitRequest{ObjectID: snapshothistory.ObjectID{byte(i + 1)}, Snapshot: snapshot, ExpectedManifestDigest: digest, Intent: snapshothistory.Intent{AttemptID: []byte("attempt" + strconv.Itoa(i)), ProviderRequestID: []byte("request" + strconv.Itoa(i)), OriginalJournal: []byte("journal" + strconv.Itoa(i)), ApprovedTargetScope: []byte(s)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = snapshothistory.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	policy := restartMetadataAccess(func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error {
		return nil
	})
	server := httptest.NewServer(h.protectedRestartMetadata(store, policy))
	defer server.Close()
	get := func() snapshothistory.RestartDiscoveryPage {
		t.Helper()
		r, _ := http.NewRequest("GET", server.URL+"/?host=demo", nil)
		r.AddCookie(cookie)
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("metadata status %d", res.StatusCode)
		}
		if strings.Contains(string(body), string(content)) || strings.Contains(string(body), "main.pkl") {
			t.Fatal("archive source disclosed")
		}
		var page snapshothistory.RestartDiscoveryPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := get()
	if len(page.Records) != 1 || page.Records[0].Locator != locator || string(page.Records[0].OriginalJournal) != "journal0" {
		t.Fatal("cold reopen lost or mixed intent metadata")
	}
	if err := h.registry.ForgetHTTPS(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := h.registry.AddContext(ctx, row, row); err != nil {
		t.Fatal(err)
	}
	page = get()
	if len(page.Records) != 0 {
		t.Fatal("replacement enrollment inherited old archive metadata")
	}
}
