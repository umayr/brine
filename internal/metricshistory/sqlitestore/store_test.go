package sqlitestore

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReopenAndRejectForeign(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "history.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 123).UTC()
	r := Record{Receipt: now, Target: "synthetic-target", Gauge: "memory_bytes", Value: 0}
	if err = s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Read(ctx, 10)
	if err != nil || len(got) != 1 || got[0] != r {
		t.Fatalf("got %#v err %v", got, err)
	}
	if err = s.Append(ctx, Record{Receipt: now, Target: "x", Gauge: "unknown"}); err == nil {
		t.Fatal("accepted unknown gauge")
	}
	foreign := filepath.Join(dir, "foreign.db")
	if err = os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(foreign, false); err == nil {
		t.Fatal("accepted foreign file")
	}
	b, err := os.ReadFile(foreign)
	if err != nil || string(b) != "foreign" {
		t.Fatal("foreign file changed")
	}
	if _, err = Open(path, true); err == nil {
		t.Fatal("created over existing file")
	}
}

func TestBoundsAndSidecarPreservation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proof.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		t     time.Time
		value int64
		valid bool
	}{
		{time.Unix(0, 0), 0, false}, {time.Unix(0, 1), 0, true}, {time.Unix(0, 1<<63-1), 1<<53 - 1, true},
		{time.Unix(0, 1<<63-1).Add(time.Nanosecond), 1, false}, {time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC), 1, false}, {time.Unix(1, 0), 1 << 53, false}, {time.Unix(1, 0), -1, false},
	}
	for _, tt := range tests {
		err = s.Append(context.Background(), Record{Receipt: tt.t, Target: "synthetic", Gauge: "count", Value: tt.value})
		if (err == nil) != tt.valid {
			t.Fatalf("%+v: %v", tt, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		side := path + suffix
		if err = os.WriteFile(side, []byte("synthetic sidecar"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Open(path, false); err == nil {
			t.Fatal("accepted sidecar")
		}
		if _, err = Open(path, true); err == nil {
			t.Fatal("created with sidecar")
		}
		b, e := os.ReadFile(path)
		if e != nil || string(b) != string(original) {
			t.Fatal("db modified")
		}
		b, e = os.ReadFile(side)
		if e != nil || string(b) != "synthetic sidecar" {
			t.Fatal("sidecar modified")
		}
		if err = os.Remove(side); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectExtraSchemaObjectPreservesBytes(t *testing.T) {
	path := newClosedProof(t)
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, createErr := db.Exec("CREATE VIEW sqliteXreport AS SELECT 1")
	closeErr := db.Close()
	if createErr != nil || closeErr != nil {
		t.Fatalf("construct foreign schema: %v; close: %v", createErr, closeErr)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	requireRejectedUnchanged(t, path, before)
}

func TestRejectHardlinkedDatabasePreservesBothPaths(t *testing.T) {
	path := newClosedProof(t)
	alias := filepath.Join(filepath.Dir(path), "alias.db")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, alias} {
		requireRejectedUnchanged(t, name, before)
	}
}

func newClosedProof(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proof.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireRejectedUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	if s, err := Open(path, false); err == nil {
		_ = s.Close()
		t.Errorf("accepted inadmissible database %q", path)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Errorf("database changed after admission attempt: %v", err)
	}
	if err := noSidecars(path); err != nil {
		t.Errorf("sidecar present after admission attempt: %v", err)
	}
}

func TestForeignSQLiteUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foreign.db")
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE foreign_data (value TEXT); INSERT INTO foreign_data VALUES ('synthetic')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path, false); err == nil {
		t.Fatal("accepted foreign SQLite")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("foreign SQLite changed")
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path, false); err == nil {
		t.Fatal("accepted public directory")
	}
}
