//go:build linux

package snapshothistory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sourceArchiveFixture(t testing.TB) Options {
	t.Helper()
	o, _ := fixture(t, 1<<20, 10)
	return initSourceArchiveForTest(t, o)
}

func initSourceArchiveForTest(t testing.TB, o Options) Options {
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

func openSourceArchiveForTest(t testing.TB, o Options, settings ...SourceArchiveLimits) (*SourceArchive, func()) {
	t.Helper()
	var limits SourceArchiveLimits
	if len(settings) != 0 {
		limits = settings[0]
	}
	a, err := OpenSourceArchive(o.ArchiveDir, o.KeyFile, o.WorkspaceDir, limits)
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
		Actor:      "synthetic-source-actor",
		Entrypoint: "synthetic-main.pkl",
		Created:    time.Date(2025, time.March, 4, 5, 6, 7, 8, time.UTC),
		Files: map[string][]byte{
			"synthetic-main.pkl": []byte("SYNTHETIC_SOURCE_ARCHIVE_CONTENT\n"),
			"lib/bytes.pkl":      {0, 0xff, '\r', '\n'},
		},
	}
}

func (a *SourceArchive) putTestRevision(scope string, r SourceRevision) (string, error) {
	ids, e := a.PutChain(context.Background(), scope, nil, []SourceRevision{r})
	if e != nil {
		return "", e
	}
	return ids[0], nil
}

func TestSourceArchiveBatchCapacity(t *testing.T) {
	o := sourceArchiveFixture(t)
	a, _ := openSourceArchiveForTest(t, o, SourceArchiveLimits{Objects: 1})
	r := sourceRevisionForTest()
	if _, e := a.PutChain(context.Background(), sourceTestScope, nil, []SourceRevision{r, r}); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	entries, err := os.ReadDir(o.ArchiveDir)
	if err != nil || len(entries) != 1 {
		t.Fatal("quota failure published a member")
	}
}
func putSourceRevisionForTest(t *testing.T, a *SourceArchive, r SourceRevision) string {
	t.Helper()
	id, err := a.putTestRevision(sourceTestScope, r)
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
			reopened, err := OpenSourceArchive(o.ArchiveDir, o.KeyFile, o.WorkspaceDir, SourceArchiveLimits{Objects: 1, Bytes: 1})
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

// Run with -benchtime=1x on a disposable Linux runner. The fixture seeds
// almost the full hard read bound; the timer covers only the write-time scan.
func BenchmarkSourceArchiveScanAtLimit(b *testing.B) {
	o := sourceArchiveFixture(b)
	a, _ := openSourceArchiveForTest(b, o)
	r := sourceRevisionForTest()
	r.Files = map[string][]byte{r.Entrypoint: make([]byte, 765<<10)}
	plain, err := json.Marshal(sourcePayload{Scope: sourceTestScope, Revision: r})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < MaxSourceArchiveObjects; i++ {
		var id ObjectID
		binary.BigEndian.PutUint32(id[:4], uint32(i+1))
		sealed, err := seal(a.storage.s.key, a.storage.objectStore, id, sourceObjectKind, plain)
		if err != nil {
			b.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(o.ArchiveDir, fmt.Sprintf("%s%x", sourceObjectPrefix, id[:])), sealed, 0600); err != nil {
			b.Fatal(err)
		}
	}
	ctx := context.Background()
	b.SetBytes(int64(len(plain)) * MaxSourceArchiveObjects)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.storage.scan(ctx, sourceTestScope, true); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSourceArchiveQuotas(t *testing.T) {
	t.Run("revision count", func(t *testing.T) {
		o := sourceArchiveFixture(t)
		a, closeArchive := openSourceArchiveForTest(t, o)
		ctx := context.Background()
		r := sourceRevisionForTest()
		var lineage []string
		for i := 0; i < DefaultSourceArchiveObjects; i++ {
			if i > 0 {
				r.Parent = lineage[i-1]
			}
			ids, err := a.PutChain(ctx, sourceTestScope, lineage, []SourceRevision{r})
			if err != nil {
				t.Fatal(err)
			}
			lineage = append(lineage, ids[0])
		}
		r.Parent = lineage[len(lineage)-1]
		if ids, err := a.PutChain(ctx, sourceTestScope, lineage, []SourceRevision{r}); len(ids) != 0 || !errors.Is(err, ErrCapacity) {
			t.Fatalf("default write limit: %v, %v", ids, err)
		}
		closeArchive()

		a, closeArchive = openSourceArchiveForTest(t, o, SourceArchiveLimits{Objects: DefaultSourceArchiveObjects + 2})
		ids, err := a.PutChain(ctx, sourceTestScope, lineage, []SourceRevision{r})
		if err != nil || len(ids) != 1 {
			t.Fatalf("raise quota on same archive: %v, %v", ids, err)
		}
		lineage = append(lineage, ids[0])
		usage, err := a.Usage(ctx)
		if err != nil || usage.Objects != len(lineage) || usage.RemainingObjects() != 1 || usage.Bytes <= 0 {
			t.Fatalf("raised quota usage: %+v, %v", usage, err)
		}
		closeArchive()

		a, _ = openSourceArchiveForTest(t, o, SourceArchiveLimits{Objects: 1, Bytes: 1})
		usage, err = a.Usage(ctx)
		if err != nil || usage.Objects != len(lineage) || usage.Bytes <= 1 || usage.RemainingObjects() != 0 || usage.RemainingBytes() != 0 {
			t.Fatalf("lowered quota usage: %+v, %v", usage, err)
		}
		last := lineage[len(lineage)-1]
		if got, err := a.Get(sourceTestScope, last); err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("over-quota archive lost its revision: %v", err)
		}
		r.Parent = last
		if ids, err := a.PutChain(ctx, sourceTestScope, lineage, []SourceRevision{r}); len(ids) != 0 || !errors.Is(err, ErrCapacity) {
			t.Fatalf("lowered quota admitted a write: %v, %v", ids, err)
		}
	})
	t.Run("stored bytes", func(t *testing.T) {
		o := sourceArchiveFixture(t)
		a, closeArchive := openSourceArchiveForTest(t, o)
		r := sourceRevisionForTest()
		id := putSourceRevisionForTest(t, a, r)
		closeArchive()
		// Leave a byte of headroom, not enough for a complete sealed revision.
		var used int64
		for _, name := range []string{sourceIdentityName, sourceObjectPrefix + id} {
			info, err := os.Stat(filepath.Join(o.ArchiveDir, name))
			if err != nil {
				t.Fatal(err)
			}
			used += info.Size()
		}
		a, _ = openSourceArchiveForTest(t, o, SourceArchiveLimits{Bytes: used + 1})
		r.Actor = "synthetic-over-quota"
		if id, err := a.putTestRevision(sourceTestScope, r); id != "" || !errors.Is(err, ErrCapacity) {
			t.Fatalf("put beyond byte quota: id=%q, err=%v, want no ID and ErrCapacity", id, err)
		}
		if _, err := a.Get(sourceTestScope, id); err != nil {
			t.Fatalf("get existing revision at byte quota: %v", err)
		}
	})
}
