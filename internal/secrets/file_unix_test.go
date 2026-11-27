//go:build linux

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLocalFileAllowlistAndFreshReads(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "home-source", "first\n\x00")
	sources := map[string]FileRef{"approved": ref}
	cfg := Config{Version: SchemaVersion, Connections: map[string]ConnectionConfig{"host": {Files: &FilesConfig{Sources: sources}}}}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if caps, err := r.Capabilities("host"); err != nil || caps != (Capabilities{Kind: KindFile}) {
		t.Fatal("unexpected file capabilities")
	}
	s := Selector{Version: SchemaVersion, Connection: "host", File: &FileSelector{Source: "approved"}}
	assertAcquisition(t, r, context.Background(), s, "first\n\x00", nil)
	// The configured map is snapshotted, but contents are never cached.
	delete(sources, "approved")
	sources["unapproved"] = ref
	writeFixture(t, dir, "home-source", "second")
	assertAcquisition(t, r, context.Background(), s, "second", nil)
	s.File.Source = "unapproved"
	assertAcquisition(t, r, context.Background(), s, "", ErrDenied)
	s.File.Source = ref.Path
	assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
	s.File.Source = "../approved"
	assertAcquisition(t, r, context.Background(), s, "", ErrInvalidSelector)
	s.File.Source = "approved"
	if err := os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	assertAcquisition(t, r, context.Background(), s, "", ErrNotFound)
}

func TestLocalFileSecurity(t *testing.T) {
	dir := t.TempDir()
	secure := writeFixture(t, dir, "secure", synthetic)
	sym := filepath.Join(dir, "link")
	if err := os.Symlink(secure.Path, sym); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "dir-link")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	open := writeFixture(t, dir, "unsafe-mode", synthetic)
	if err := os.Chmod(open.Path, 0o640); err != nil {
		t.Fatal(err)
	}
	writable := filepath.Join(dir, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	inWritable := writeFixture(t, writable, "file", synthetic)
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	hardTarget := writeFixture(t, dir, "hard-target", synthetic)
	hardlink := filepath.Join(dir, "hardlink")
	if err := os.Link(hardTarget.Path, hardlink); err != nil {
		t.Fatal(err)
	}
	readOnly := writeFixture(t, dir, "read-only", synthetic)
	if err := os.Chmod(readOnly.Path, 0o400); err != nil {
		t.Fatal(err)
	}
	oversize := writeFixture(t, dir, "oversize", strings.Repeat("a", MaxValueBytes+1))
	cfg := Config{Version: SchemaVersion, Connections: map[string]ConnectionConfig{
		"host": {Files: &FilesConfig{Sources: map[string]FileRef{
			"terminal-link":  {Path: sym},
			"directory-link": {Path: filepath.Join(linkDir, filepath.Base(secure.Path))},
			"readable":       open,
			"writable":       inWritable,
			"fifo":           {Path: fifo},
			"hardlink":       {Path: hardlink},
			"directory":      {Path: writable},
			"read-only":      readOnly,
			"oversize":       oversize,
		}}},
	}}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := Selector{Version: SchemaVersion, Connection: "host", File: &FileSelector{}}
	for _, name := range []string{"terminal-link", "directory-link", "readable", "writable", "fifo", "hardlink", "directory"} {
		t.Run(name, func(t *testing.T) {
			s.File.Source = name
			assertAcquisition(t, r, context.Background(), s, "", ErrDenied)
		})
	}
	s.File.Source = "read-only"
	assertAcquisition(t, r, context.Background(), s, synthetic, nil)
	s.File.Source = "oversize"
	assertAcquisition(t, r, context.Background(), s, "", ErrTooLarge)
}

func TestLocalFileOwnerPolicy(t *testing.T) {
	const uid = 1001
	if safeFile(&syscall.Stat_t{Mode: syscall.S_IFREG | 0o600, Uid: 1002, Nlink: 1}, uid) ||
		safeDirectory(&syscall.Stat_t{Mode: syscall.S_IFDIR | 0o700, Uid: 1002}, uid) ||
		safeDirectory(&syscall.Stat_t{Mode: syscall.S_IFDIR | 0o1777, Uid: uid}, uid) {
		t.Fatal("untrusted owner or writable directory accepted")
	}
}
