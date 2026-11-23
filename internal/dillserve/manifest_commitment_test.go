package dillserve

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/umayr/brine/internal/projectsnapshot"
)

func TestSnapshotManifestDigest(t *testing.T) {
	file := func(path, content string) projectsnapshot.File {
		b := []byte(content)
		return projectsnapshot.File{Path: path, Content: b, SHA256: sha256.Sum256(b)}
	}
	const raw = `{"schema":"dill.snapshot.manifest.v1","entrypoint":"main.pkl","files":[{"path":"assets/a\u0026b","length":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},{"path":"main.pkl","length":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`
	want := sha256.Sum256([]byte(raw))
	ctx := context.Background()
	base := func() projectsnapshot.Snapshot {
		return projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{file("assets/a&b", ""), file("main.pkl", "abc")}}
	}
	t.Run("canonical receiver manifest", func(t *testing.T) {
		if _, err := validateManifest(ctx, []byte(raw), want); err != nil {
			t.Fatalf("fixture is not a valid catalog manifest: %v", err)
		}
		got, err := SnapshotManifestDigest(ctx, base())
		if err != nil || got != want {
			t.Fatalf("digest=%x error=%v, want %x", got, err, want)
		}
	})
	t.Run("ordering is canonical without reordering the caller", func(t *testing.T) {
		s := base()
		s.Files[0], s.Files[1] = s.Files[1], s.Files[0]
		got, err := SnapshotManifestDigest(ctx, s)
		if err != nil || got != want || s.Files[0].Path != s.Entrypoint {
			t.Fatalf("digest=%x error=%v or input order changed", got, err)
		}
	})
	for _, tc := range []struct {
		name   string
		change func(*projectsnapshot.Snapshot)
	}{
		{"entrypoint", func(s *projectsnapshot.Snapshot) { s.Entrypoint = s.Files[0].Path }},
		{"path", func(s *projectsnapshot.Snapshot) { s.Files[0].Path = "assets/other" }},
		{"content", func(s *projectsnapshot.Snapshot) { s.Files[1] = file("main.pkl", "xyz") }},
	} {
		t.Run(tc.name+" changes commitment", func(t *testing.T) {
			s := base()
			tc.change(&s)
			if got, err := SnapshotManifestDigest(ctx, s); err != nil || got == want {
				t.Fatalf("digest=%x error=%v, want a changed commitment", got, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*projectsnapshot.Snapshot)
	}{
		{"bytes no longer match recorded hash", func(s *projectsnapshot.Snapshot) { s.Files[1].Content[0] ^= 1 }},
		{"incompatible path", func(s *projectsnapshot.Snapshot) { s.Files[0].Path = "../escape" }},
		{"missing entrypoint", func(s *projectsnapshot.Snapshot) { s.Entrypoint = "missing.pkl" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.change(&s)
			if got, err := SnapshotManifestDigest(ctx, s); err == nil || got != ([sha256.Size]byte{}) {
				t.Fatalf("digest=%x error=%v, want rejection", got, err)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if got, err := SnapshotManifestDigest(canceled, base()); !errors.Is(err, context.Canceled) || got != ([sha256.Size]byte{}) {
			t.Fatalf("digest=%x error=%v, want cancellation", got, err)
		}
	})
}
