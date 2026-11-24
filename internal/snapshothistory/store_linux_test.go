//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectsnapshot"
)

var fixtureHome = regexp.MustCompile(`^/brine-go-test-home\.[A-Za-z0-9]{8}$`)

func fixture(t *testing.T, maxBytes int64, maxOps int) (Options, func()) {
	t.Helper()
	home := os.Getenv("HOME")
	if !fixtureHome.MatchString(home) {
		t.Fatal("archive tests require the CI-generated private HOME directly under /")
	}
	dir, err := openPrivateDirectory(home)
	if err != nil {
		t.Fatal("CI fixture HOME is not private and trusted")
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(home, "snapshothistory-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	archive, keyDir, workspace := filepath.Join(base, "archive"), filepath.Join(base, "key"), filepath.Join(base, "workspace")
	for _, dir := range []string{archive, keyDir, workspace} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	key := bytes.Repeat([]byte{0x6a}, 32)
	if err := os.WriteFile(filepath.Join(keyDir, "archive.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	var storeID [32]byte
	copy(storeID[:], []byte("synthetic-store-identity-v1-00000"))
	o := Options{ArchiveDir: archive, KeyFile: filepath.Join(keyDir, "archive.key"), WorkspaceDir: workspace, StoreID: storeID, MaxBytes: maxBytes, MaxOperations: maxOps}
	if err := Initialize(context.Background(), o); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return o, func() {}
}
func request(n byte) CommitRequest {
	content := []byte("SYNTHETIC_SECRET_REVIEWED_BYTES_" + string([]byte{n}))
	f := projectsnapshot.File{Path: "main.pkl", Content: content, SHA256: sha256.Sum256(content)}
	var id ObjectID
	id[0] = n
	id[31] = 1
	snapshot := projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{f}}
	digest, err := dillserve.SnapshotManifestDigest(context.Background(), snapshot)
	if err != nil {
		panic(err)
	}
	return CommitRequest{ObjectID: id, Intent: Intent{AttemptID: []byte("attempt-" + string([]byte{n})), ProviderRequestID: []byte("request-" + string([]byte{n})), OriginalJournal: []byte("journal-" + string([]byte{n})), ApprovedTargetScope: []byte("scope-" + string([]byte{n}))}, ExpectedManifestDigest: digest, Snapshot: snapshot}
}

func TestRoundTripExactBytesAndAuthenticatedContext(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	r := request(1)
	opened, err := s.Commit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(context.Background(), opened)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Snapshot.Files[0].Content, r.Snapshot.Files[0].Content) || !bytes.Equal(got.Intent.OriginalJournal, r.Intent.OriginalJournal) || !bytes.Equal(got.Intent.ApprovedTargetScope, r.Intent.ApprovedTargetScope) {
		t.Fatal("exact reviewed bytes or intent changed")
	}
	wrong := opened
	wrong.Intent.ProviderRequestID = []byte("different-request")
	if _, err := s.Open(context.Background(), wrong); err == nil {
		t.Fatal("wrong authenticated context accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Open(context.Background(), opened); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsTamperSubstitutionWrongKeyAndPathOverlap(t *testing.T) {
	t.Run("tamper", func(t *testing.T) {
		o, _ := fixture(t, 1<<20, 10)
		s, err := Open(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		a := request(2)
		if _, err := s.Commit(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		s.Close()
		path := filepath.Join(o.ArchiveDir, objectName(a.ObjectID))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data[len(data)-1] ^= 1
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), o); err == nil {
			t.Fatal("tampered archive opened")
		}
	})
	t.Run("substitution", func(t *testing.T) {
		o, _ := fixture(t, 1<<20, 10)
		s, err := Open(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		a, b := request(3), request(4)
		if _, err := s.Commit(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Commit(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		s.Close()
		other, err := os.ReadFile(filepath.Join(o.ArchiveDir, objectName(b.ObjectID)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.ArchiveDir, objectName(a.ObjectID)), other, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), o); err == nil {
			t.Fatal("substituted ciphertext opened")
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		o, _ := fixture(t, 1<<20, 10)
		// A replacement is an external unsafe-state event; the package itself
		// never writes a replacement, and the identity object rejects this key.
		if err := os.WriteFile(o.KeyFile, bytes.Repeat([]byte{0x55}, 32), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), o); err == nil {
			t.Fatal("wrong key opened")
		}
	})
	o2, _ := fixture(t, 1<<20, 10)
	o2.WorkspaceDir = o2.ArchiveDir
	if _, err := Open(context.Background(), o2); err == nil {
		t.Fatal("archive/workspace overlap accepted")
	}
}

func TestCommitRequiresIndependentManifestAndCompleteIntent(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, mutate := range []func(*CommitRequest){
		func(r *CommitRequest) { r.ExpectedManifestDigest = [32]byte{} },
		func(r *CommitRequest) { r.Intent.OriginalJournal = nil },
		func(r *CommitRequest) { r.Intent.ApprovedTargetScope = nil },
	} {
		r := request(20)
		mutate(&r)
		if _, err := s.Commit(context.Background(), r); err == nil {
			t.Fatal("incomplete or mismatched request committed")
		}
	}
}

func TestRejectsDuplicateIDsWithinOriginalJournal(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := request(21)
	if _, err := s.Commit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	attempt := request(22)
	attempt.Intent.OriginalJournal = append([]byte(nil), first.Intent.OriginalJournal...)
	attempt.Intent.AttemptID = append([]byte(nil), first.Intent.AttemptID...)
	if _, err := s.Commit(context.Background(), attempt); !errors.Is(err, ErrDuplicateIntent) {
		t.Fatalf("duplicate attempt=%v", err)
	}
	provider := request(23)
	provider.Intent.OriginalJournal = append([]byte(nil), first.Intent.OriginalJournal...)
	provider.Intent.ProviderRequestID = append([]byte(nil), first.Intent.ProviderRequestID...)
	if _, err := s.Commit(context.Background(), provider); !errors.Is(err, ErrDuplicateIntent) {
		t.Fatalf("duplicate provider request=%v", err)
	}
	otherJournal := request(24)
	otherJournal.Intent.AttemptID = append([]byte(nil), first.Intent.AttemptID...)
	if _, err := s.Commit(context.Background(), otherJournal); err != nil {
		t.Fatalf("same opaque ID in another journal=%v", err)
	}
}

func TestNoOverwrite(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := request(6)
	opened, err := s.Commit(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := request(6)
	second.Intent.OriginalJournal = []byte("replacement journal")
	if _, err := s.Commit(context.Background(), second); !errors.Is(err, ErrExists) {
		t.Fatalf("replacement=%v, want exists", err)
	}
	got, err := s.Open(context.Background(), opened)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Intent.OriginalJournal, first.Intent.OriginalJournal) {
		t.Fatal("existing object overwritten")
	}
}

func TestCapacityReservationAndNoPlaintextSideFiles(t *testing.T) {
	o, _ := fixture(t, 1<<20, 1)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, n := range []byte{4, 5} {
		wg.Add(1)
		go func(n byte) { defer wg.Done(); _, e := s.Commit(context.Background(), request(n)); errs <- e }(n)
	}
	wg.Wait()
	close(errs)
	success := 0
	capacity := 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrCapacity) {
			capacity++
		} else {
			t.Fatalf("unexpected concurrent result: %v", e)
		}
	}
	if success != 1 || capacity != 1 {
		t.Fatalf("success=%d capacity=%d", success, capacity)
	}
	entries, err := os.ReadDir(o.ArchiveDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != identityName && !validObjectName(name) && !validPendingName(name) {
			t.Fatalf("unexpected side file %q", name)
		}
		if !entry.IsDir() {
			b, e := os.ReadFile(filepath.Join(o.ArchiveDir, name))
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(b, []byte("SYNTHETIC_SECRET_REVIEWED_BYTES")) {
				t.Fatalf("plaintext in archive file %q", name)
			}
		}
	}
}

func TestReducedQuotaPreservesReadableObjects(t *testing.T) {
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	r := request(8)
	opened, err := s.Commit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	o.MaxBytes, o.MaxOperations = 1, 1
	s, err = Open(context.Background(), o)
	if err != nil {
		t.Fatalf("reopen with reduced cap: %v", err)
	}
	defer s.Close()
	if _, err := s.Open(context.Background(), opened); err != nil {
		t.Fatalf("read retained object: %v", err)
	}
	if _, err := s.Commit(context.Background(), request(7)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("commit under reduced cap=%v, want capacity", err)
	}
}

func TestFaultsNeverReturnDurableSuccess(t *testing.T) {
	defer func() { testFault = nil; testDirectorySync = nil }()
	for _, tc := range []struct {
		name, point   string
		indeterminate bool
	}{
		{"before-sync", "before-file-sync", false}, {"before-rename", "before-rename", false}, {"after-rename", "after-rename-before-directory-sync", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := fixture(t, 1<<20, 10)
			s, err := Open(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			testFault = func(point string) error {
				if point == tc.point {
					return errors.New("synthetic fault")
				}
				return nil
			}
			_, err = s.Commit(context.Background(), request(9))
			if err == nil {
				t.Fatal("uncertain write returned success")
			}
			if tc.indeterminate && !errors.Is(err, ErrIndeterminate) {
				t.Fatalf("error=%v, want indeterminate", err)
			}
			if !tc.indeterminate && errors.Is(err, ErrIndeterminate) {
				t.Fatalf("error=%v, want pre-publication failure", err)
			}
			testFault = nil
			s.Close()
			// A pre-rename fault retains a charged pending object; a post-rename
			// fault retains the object. Neither path reports durable success.
			if reopened, e := Open(context.Background(), o); e != nil {
				t.Fatalf("reopen failed: %v", e)
			} else {
				reopened.Close()
			}
		})
	}
}

func TestPendingRemnantConsumesOperationCapacity(t *testing.T) {
	defer func() { testFault = nil }()
	o, _ := fixture(t, 1<<20, 1)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testFault = func(point string) error {
		if point == "before-rename" {
			return errors.New("synthetic fault")
		}
		return nil
	}
	if _, err := s.Commit(context.Background(), request(26)); err == nil {
		t.Fatal("pre-rename fault returned success")
	}
	testFault = nil
	if _, err := s.Commit(context.Background(), request(27)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("pending remnant was not charged: %v", err)
	}
}

func TestDirectorySyncErrorIsIndeterminate(t *testing.T) {
	defer func() { testDirectorySync = nil }()
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	called := false
	testDirectorySync = func(fd int) error { called = fd >= 0; return errors.New("synthetic directory fsync error") }
	if _, err := s.Commit(context.Background(), request(25)); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("directory sync=%v", err)
	}
	if !called {
		t.Fatal("directory sync seam was not called")
	}
}
