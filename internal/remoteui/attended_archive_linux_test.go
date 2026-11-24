//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
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
	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/sys/unix"
)

func realAttendedArchiveHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("HOME")
	if !regexp.MustCompile(`^/brine-go-test-home\.[A-Za-z0-9]{8}$`).MatchString(home) {
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatal("archive tests require the CI-generated private HOME")
		}
		t.Skip("private archive fixture unavailable; run bash scripts/ci-test-go.sh on disposable Linux")
	}
	// Recheck the CI root and its single child before creating any fixture.
	for _, path := range []string{"/", home} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal("cannot open generated fixture ancestry")
		}
		var st unix.Stat_t
		statErr := unix.Fstat(fd, &st)
		_, access := unix.Fgetxattr(fd, "system.posix_acl_access", nil)
		_, inherited := unix.Fgetxattr(fd, "system.posix_acl_default", nil)
		_ = unix.Close(fd)
		if statErr != nil || !errors.Is(access, unix.ENODATA) || !errors.Is(inherited, unix.ENODATA) ||
			st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) ||
			(path == home && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700)) {
			t.Fatal("unsafe generated fixture ancestry")
		}
	}
	base, err := os.MkdirTemp(home, "attended-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	return base
}

type realAttendedArchiveProbe struct {
	*snapshothistory.Store
	ref                                    snapshothistory.OpenRequest
	commitCalls, markerCalls, receiptCalls int
	markerErr, receiptErr                  error
}

func (p *realAttendedArchiveProbe) Commit(ctx context.Context, r snapshothistory.CommitRequest) (snapshothistory.OpenRequest, error) {
	p.commitCalls++
	ref, err := p.Store.Commit(ctx, r)
	p.ref = ref
	return ref, err
}
func (p *realAttendedArchiveProbe) MarkDeliveryUnknown(ctx context.Context, r snapshothistory.OpenRequest, at time.Time) error {
	p.markerCalls++
	p.markerErr = p.Store.MarkDeliveryUnknown(ctx, r, at)
	return p.markerErr
}
func (p *realAttendedArchiveProbe) RecordTerminalObservation(ctx context.Context, r snapshothistory.OpenRequest, o snapshothistory.TerminalObservation) error {
	p.receiptCalls++
	p.receiptErr = p.Store.RecordTerminalObservation(ctx, r, o)
	return p.receiptErr
}

type linuxAttendedProvider struct {
	deploymentStager
	probe *realAttendedArchiveProbe
	t     *testing.T
	calls int
}

func (*linuxAttendedProvider) Close() error { return nil }
func (p *linuxAttendedProvider) ApplyJournalDeployment(ctx context.Context, _, _, _, stack, _, request, _, journal string) (agentclient.DeploymentStatus, error) {
	p.calls++
	got, err := p.probe.Store.ReadEvidence(ctx, p.probe.ref)
	if err != nil || got.State != snapshothistory.DeliveryUnknown || request != string(p.probe.ref.Intent.ProviderRequestID) || journal != string(p.probe.ref.Intent.OriginalJournal) {
		p.t.Fatalf("provider called without matching durable uncertainty marker: %v", err)
	}
	return agentclient.DeploymentStatus{RequestID: request, Stack: stack, State: "succeeded", Effects: "complete", OperationID: "op_" + strings.Repeat("e", 32)}, nil
}

func TestAttendedRealArchiveCapacityAndReopenNeverReplay(t *testing.T) {
	ctx := context.Background()
	root := realAttendedArchiveHome(t)
	newOptions := func(name string) snapshothistory.Options {
		t.Helper()
		base := filepath.Join(root, name)
		for _, n := range []string{"archive", "key", "workspace"} {
			if err := os.MkdirAll(filepath.Join(base, n), 0700); err != nil {
				t.Fatal(err)
			}
		}
		key := filepath.Join(base, "key", "synthetic.key")
		if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
			t.Fatal(err)
		}
		return snapshothistory.Options{ArchiveDir: filepath.Join(base, "archive"), KeyFile: key, WorkspaceDir: filepath.Join(base, "workspace"), StoreID: sha256.Sum256([]byte(name))}
	}
	var record targetregistry.Record
	binding := attendedBinding{target: targetDigest(record), scope: "synthetic-scope", revision: strings.Repeat("a", 64), contractVersion: "dill/0.9.0", grants: attendedGrants, distinct: true}
	content := []byte("synthetic-reviewed")
	snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}}}
	digest, err := dillserve.SnapshotManifestDigest(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("%x", digest)
	attempt := strings.Repeat("1", 32)
	request := strings.Repeat("2", 32)
	// Measure identity plus intent using the same encoded lengths as submission.
	probeOpts := newOptions("measure")
	if err := snapshothistory.Initialize(ctx, probeOpts); err != nil {
		t.Fatal(err)
	}
	measure, err := snapshothistory.Open(ctx, probeOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer measure.Close()
	ref, err := measure.Commit(ctx, snapshothistory.CommitRequest{ObjectID: sha256.Sum256([]byte("generated-id")), ExpectedManifestDigest: digest, Snapshot: snapshot, Intent: snapshothistory.Intent{
		AttemptID: []byte(attempt), ProviderRequestID: []byte(request), OriginalJournal: []byte(attendedJournal), ApprovedTargetScope: []byte(binding.scope),
		Attended: &snapshothistory.AttendedIntent{Target: binding.target, AuthorityRevision: binding.revision, Stack: "synthetic", Mode: "create", Stage: attendedStage, Selection: attendedSelection},
	}})
	if err != nil {
		t.Fatal(err)
	}
	usage := func(dir string) int64 {
		t.Helper()
		var size int64
		names, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			info, err := n.Info()
			if err != nil {
				t.Fatal(err)
			}
			size += info.Size()
		}
		return size
	}
	intentBytes := usage(probeOpts.ArchiveDir)
	if err := measure.MarkDeliveryUnknown(ctx, ref, time.Unix(1795488000, 123456789)); err != nil {
		t.Fatal(err)
	}
	markerBytes := usage(probeOpts.ArchiveDir)
	for _, tc := range []struct {
		name    string
		cap     int64
		want    snapshothistory.DeliveryState
		calls   int
		message string
	}{
		{"marker-capacity", intentBytes, snapshothistory.LocalIntentOnly, 0, "No operation was sent"},
		{"receipt-capacity", markerBytes, snapshothistory.DeliveryUnknown, 1, "Live observation only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := newOptions(tc.name)
			opts.MaxBytes = tc.cap
			if err := snapshothistory.Initialize(ctx, opts); err != nil {
				t.Fatal(err)
			}
			s, err := snapshothistory.Open(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			probe := &realAttendedArchiveProbe{Store: s}
			provider := &linuxAttendedProvider{t: t, probe: probe}
			history := &attendedHistory{archive: probe, open: func(context.Context, targetregistry.Record) (attendedConnection, error) {
				return attendedConnection{client: provider, binding: binding, epoch: "synthetic"}, nil
			}}
			h := &Deployment{history: history}
			r := deploymentReview{Stack: "synthetic", Mode: "create", StageID: attendedStage, Manifest: manifest, Selection: attendedSelection, TargetDigest: binding.target, Expiry: time.Now().Add(time.Minute), Attended: &attendedReview{snapshot: snapshot, binding: binding, manifest: digest, journal: attendedJournal, requestID: request}}
			w := httptest.NewRecorder()
			h.submitAttended(ctx, w, deploymentPage{ID: attempt, Host: "synthetic"}, r, record)
			if probe.commitCalls != 1 || probe.markerCalls != 1 || provider.calls != tc.calls || probe.receiptCalls != tc.calls || !strings.Contains(w.Body.String(), tc.message) || strings.Contains(w.Body.String(), "Verified terminal receipt saved") {
				t.Fatalf("wrong gate: %d/%d/%d/%d HTTP %d", probe.commitCalls, probe.markerCalls, provider.calls, probe.receiptCalls, w.Code)
			}
			if tc.calls == 0 && !errors.Is(probe.markerErr, snapshothistory.ErrCapacity) || tc.calls == 1 && (!errors.Is(probe.receiptErr, snapshothistory.ErrCapacity) || !strings.Contains(w.Body.String(), "State succeeded")) {
				t.Fatal("expected exact real-store capacity boundary")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := snapshothistory.Open(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got, err := reopened.ReadEvidence(ctx, probe.ref); err != nil || got.State != tc.want || got.Receipt != nil {
				t.Fatalf("wrong reopened evidence: %s %v", got.State, err)
			}
			// A stale confirmation against a fresh handler cannot resume a
			// retained intent or unknown marker; there is no recovery sender.
			history.archive = reopened
			authenticator, err := auth.New(auth.Config{User: "admin", Password: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("user=admin&password=synthetic"))
			login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			loginResult := httptest.NewRecorder()
			authenticator.HandleLogin(loginResult, login)
			cookies := loginResult.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("fixture login HTTP %d", loginResult.Code)
			}
			fresh := &Deployment{auth: authenticator, history: history, pending: make(map[string]deploymentReview)}
			get := httptest.NewRequest(http.MethodGet, deployPath+"?host=synthetic", nil)
			get.AddCookie(cookies[0])
			formPage := httptest.NewRecorder()
			fresh.Protected().ServeHTTP(formPage, get)
			csrf := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(formPage.Body.String())
			if len(csrf) != 2 {
				t.Fatal("fresh authenticated handler did not return CSRF token")
			}
			form := url.Values{"id": {attempt}, "confirm": {"create"}, "_csrf": {csrf[1]}}
			post := httptest.NewRequest(http.MethodPost, deployPath+"/create", strings.NewReader(form.Encode()))
			post.AddCookie(cookies[0])
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			replay := httptest.NewRecorder()
			fresh.Protected().ServeHTTP(replay, post)
			if replay.Code != http.StatusConflict || !strings.Contains(replay.Body.String(), "Missing, expired, or already used confirmation") || provider.calls != tc.calls {
				t.Fatalf("reopen replay was not refused by handler: HTTP %d", replay.Code)
			}
		})
	}
}
