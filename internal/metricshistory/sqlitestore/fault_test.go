package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestPageLimitPreservesPriorRows(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "proof.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	r := Record{Receipt: time.Unix(1700000000, 0).UTC(), Target: strings.Repeat("x", 256), Gauge: "count", Value: 0}
	if err = s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	var pages, cap int
	if err = s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(fmt.Sprintf("PRAGMA max_page_count=%d", pages)).Scan(&cap); err != nil || cap != pages {
		t.Fatalf("cap %d pages %d: %v", cap, pages, err)
	}
	acknowledged := 1
	var full bool
	for i := 0; i < 1000; i++ {
		err = s.Append(ctx, r)
		if err != nil {
			var se *sqlite.Error
			if !errors.As(err, &se) || se.Code()&255 != 13 {
				t.Fatalf("expected SQLITE_FULL: %v", err)
			}
			full = true
			break
		}
		acknowledged++
	}
	if !full {
		t.Fatal("page cap did not trigger SQLITE_FULL")
	}
	got, err := s.Read(ctx, 1000)
	if err != nil || len(got) != acknowledged {
		t.Fatalf("got %d want %d: %v", len(got), acknowledged, err)
	}
	for _, v := range got {
		if v != r {
			t.Fatalf("prior row changed: %+v", v)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.Read(ctx, 1000)
	if err != nil || len(got) != acknowledged {
		t.Fatalf("reopen got %d want %d: %v", len(got), acknowledged, err)
	}
}

func TestAcknowledgedAbruptExit(t *testing.T) {
	if path := os.Getenv("SQLITESTORE_CHILD_PATH"); path != "" {
		s, err := Open(path, true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err = s.Append(context.Background(), Record{Receipt: time.Unix(1700000000, 0).UTC(), Target: "synthetic", Gauge: "count", Value: 0}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(23) // committed but deliberately never closed
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "child.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAcknowledgedAbruptExit$")
	cmd.Env = append(os.Environ(), "SQLITESTORE_CHILD_PATH="+path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child: %v, %s", err, output)
	}
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Read(context.Background(), 10)
	if err != nil || len(got) != 1 || got[0].Target != "synthetic" || got[0].Value != 0 {
		t.Fatalf("reopen: %+v %v", got, err)
	}
}
