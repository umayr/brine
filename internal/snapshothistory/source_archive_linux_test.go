//go:build linux

package snapshothistory

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sourceArchiveFixture(t *testing.T) Options {
	t.Helper()
	o, _ := fixture(t, 1<<20, 10)
	return initSourceArchiveForTest(t, o)
}

func initSourceArchiveForTest(t *testing.T, o Options) Options {
	t.Helper()
	// Use a fresh sibling of the snapshot archive under the same trusted base.
	dir, err := os.MkdirTemp(filepath.Dir(o.ArchiveDir), "source-")
	if err != nil {
		t.Fatal(err)
	}
	o.ArchiveDir = dir
	if err := InitializeSourceArchive(o.ArchiveDir, o.KeyFile, o.WorkspaceDir); err != nil {
		t.Fatalf("initialize source archive: %v", err)
	}
	return o
}

func openSourceArchiveForTest(t *testing.T, o Options) (*SourceArchive, func()) {
	t.Helper()
	a, err := OpenSourceArchive(o.ArchiveDir, o.KeyFile, o.WorkspaceDir)
	if err != nil {
		t.Fatalf("open source archive: %v", err)
	}
	closed := false
	closeArchive := func() {
		if !closed {
			closed = true
			if err := a.Close(); err != nil {
				t.Errorf("close source archive: %v", err)
			}
		}
	}
	t.Cleanup(closeArchive)
	return a, closeArchive
}

var sourceTestScope = strings.Repeat("a", 64)

func sourceRevisionForTest() SourceRevision {
	return SourceRevision{
		Parent:       strings.Repeat("1", 64),
		RestoredFrom: strings.Repeat("2", 64),
		Actor:        "synthetic-source-actor",
		Entrypoint:   "synthetic-main.pkl",
		Created:      time.Date(2025, time.March, 4, 5, 6, 7, 8, time.UTC),
		Files: map[string][]byte{
			"synthetic-main.pkl": []byte("SYNTHETIC_SOURCE_ARCHIVE_CONTENT\n"),
			"lib/bytes.pkl":      {0, 0xff, '\r', '\n'},
		},
	}
}

func putSourceRevisionForTest(t *testing.T, a *SourceArchive, r SourceRevision) string {
	t.Helper()
	id, err := a.Put(sourceTestScope, r)
	if err != nil {
		t.Fatalf("put source revision: %v", err)
	}
	return id
}

func TestSourceArchiveRoundTrip(t *testing.T) {
	o := sourceArchiveFixture(t)
	a, closeArchive := openSourceArchiveForTest(t, o)
	want := sourceRevisionForTest()
	id := putSourceRevisionForTest(t, a, want)
	closeArchive()

	entries, err := os.ReadDir(o.ArchiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("source archive has %d entries, want only identity and revision", len(entries))
	}
	content := want.Files[want.Entrypoint]
	for _, entry := range entries {
		name := entry.Name()
		if name != sourceIdentityName && name != sourceObjectPrefix+id {
			t.Fatalf("unexpected archive entry %q", name)
		}
		sealed, err := os.ReadFile(filepath.Join(o.ArchiveDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range [][]byte{
			[]byte(sourceTestScope), []byte(want.Actor), []byte(want.Entrypoint),
			content, []byte(base64.StdEncoding.EncodeToString(content)),
		} {
			if bytes.Contains(sealed, marker) {
				t.Fatalf("source plaintext present in %q", name)
			}
		}
	}

	a, _ = openSourceArchiveForTest(t, o)
	if _, err := a.Get(strings.Repeat("b", 64), id); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get with wrong scope: %v, want ErrUnavailable", err)
	}
	got, err := a.Get(sourceTestScope, id)
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("source revision metadata or bytes changed across reopen")
	}
}

func TestSourceArchiveRejectsCorruption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Options, string)
	}{
		{"swapped ID", func(t *testing.T, o *Options, id string) {
			otherID := strings.Repeat("b", 64)
			if id == otherID {
				otherID = strings.Repeat("c", 64)
			}
			if err := os.Rename(filepath.Join(o.ArchiveDir, sourceObjectPrefix+id), filepath.Join(o.ArchiveDir, sourceObjectPrefix+otherID)); err != nil {
				t.Fatal(err)
			}
		}},
		{"swapped archive", func(t *testing.T, o *Options, id string) {
			// Keep the key and object ID unchanged; the destination has its own identity.
			name := sourceObjectPrefix + id
			sealed, err := os.ReadFile(filepath.Join(o.ArchiveDir, name))
			if err != nil {
				t.Fatal(err)
			}
			*o = initSourceArchiveForTest(t, *o)
			if err := os.WriteFile(filepath.Join(o.ArchiveDir, name), sealed, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing identity", func(t *testing.T, o *Options, _ string) {
			if err := os.Remove(filepath.Join(o.ArchiveDir, sourceIdentityName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed member", func(t *testing.T, o *Options, _ string) {
			if err := os.WriteFile(filepath.Join(o.ArchiveDir, sourceObjectPrefix+"invalid-id"), []byte("invalid archive member"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := sourceArchiveFixture(t)
			a, closeArchive := openSourceArchiveForTest(t, o)
			id := putSourceRevisionForTest(t, a, sourceRevisionForTest())
			closeArchive()
			tc.mutate(t, &o, id)
			reopened, err := OpenSourceArchive(o.ArchiveDir, o.KeyFile, o.WorkspaceDir)
			if reopened != nil {
				if err := reopened.Close(); err != nil {
					t.Errorf("close unexpected handle: %v", err)
				}
				t.Error("corrupt archive returned a handle")
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("open corrupt source archive: %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestSourceArchiveQuotas(t *testing.T) {
	t.Run("revision count", func(t *testing.T) {
		a, _ := openSourceArchiveForTest(t, sourceArchiveFixture(t))
		r := sourceRevisionForTest()
		for i := 0; i < sourceMaxObjects; i++ {
			r.Actor = fmt.Sprintf("synthetic-actor-%d", i)
			putSourceRevisionForTest(t, a, r)
		}
		r.Actor = "synthetic-over-quota"
		if id, err := a.Put(sourceTestScope, r); id != "" || !errors.Is(err, ErrCapacity) {
			t.Fatalf("put beyond revision quota: id=%q, err=%v, want no ID and ErrCapacity", id, err)
		}
	})
	t.Run("stored bytes", func(t *testing.T) {
		o := sourceArchiveFixture(t)
		a, _ := openSourceArchiveForTest(t, o)
		r := sourceRevisionForTest()
		id := putSourceRevisionForTest(t, a, r)
		// Lower the limit to current disk usage to exercise the exact boundary
		// without writing hundreds of MiB in CI.
		var used int64
		for _, name := range []string{sourceIdentityName, sourceObjectPrefix + id} {
			info, err := os.Stat(filepath.Join(o.ArchiveDir, name))
			if err != nil {
				t.Fatal(err)
			}
			used += info.Size()
		}
		a.storage.s.maxBytes = used
		r.Actor = "synthetic-over-quota"
		if id, err := a.Put(sourceTestScope, r); id != "" || !errors.Is(err, ErrCapacity) {
			t.Fatalf("put beyond byte quota: id=%q, err=%v, want no ID and ErrCapacity", id, err)
		}
		if _, err := a.Get(sourceTestScope, id); err != nil {
			t.Fatalf("get existing revision at byte quota: %v", err)
		}
	})
}
