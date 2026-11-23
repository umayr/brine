//go:build linux && sqlitequalification

package sqlitestore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func qualificationEnabled(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 65532 || os.Getegid() != 65532 || os.Getenv("TMPDIR") != "/fixtures" || os.Args[0] != "/sqlitestore.test" {
		t.Fatal("not the reviewed synthetic sandbox")
	}
}

func TestQualificationIndependentProcessOwner(t *testing.T) {
	qualificationEnabled(t)
	dir := persistentFixture(t)
	child := startQualificationChild(t, dir, "owner")
	path := filepath.Join(dir, HistoryFilename)
	before := qualificationBytes(t, path)
	for _, create := range []bool{false, true} {
		s, err := OpenPersistent(dir, create)
		if s != nil {
			_ = s.Close()
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			t.Fatalf("expected lock contention, got %v", err)
		}
	}
	if string(qualificationBytes(t, path)) != string(before) || noSidecars(path) != nil {
		t.Fatal("contender changed store or created sidecar")
	}
	child.finish(t)
	assertQualificationBaseline(t, dir)
}

func TestQualificationExitBeforeCommit(t *testing.T) {
	qualificationEnabled(t)
	dir := persistentFixture(t)
	child := startQualificationChild(t, dir, "interrupted")
	path := filepath.Join(dir, HistoryFilename)
	before := qualificationBytes(t, path)
	journal := qualificationBytes(t, path+"-journal")
	child.finish(t)
	for i := 0; i < 2; i++ {
		s, err := OpenPersistent(dir, false)
		if err == nil {
			_ = s.Close()
			t.Fatal("admitted a database with interrupted transaction journal")
		}
	}
	if string(qualificationBytes(t, path)) != string(before) || string(qualificationBytes(t, path+"-journal")) != string(journal) {
		t.Fatal("database or interrupted journal changed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatal("unexpected additional sidecar")
		}
	}
}

func TestQualificationChild(t *testing.T) {
	qualificationEnabled(t)
	if len(os.Args) != 6 || os.Args[3] != "--" {
		t.Fatal("qualification child must be called only by the barrier tests")
	}
	dir, mode := os.Args[4], os.Args[5]
	if !strings.HasPrefix(dir, "/fixtures/TestQualification") || filepath.Clean(dir) != dir {
		t.Fatal("unexpected synthetic fixture path")
	}
	s, err := OpenPersistent(dir, true)
	if err != nil {
		os.Exit(40)
	}
	if err := s.Append(context.Background(), qualificationBaseline()); err != nil {
		os.Exit(41)
	}
	switch mode {
	case "owner":
	case "interrupted":
		tx, err := s.store.db.Begin()
		if err != nil {
			os.Exit(42)
		}
		if _, err := tx.Exec("UPDATE observations SET value = 27"); err != nil {
			os.Exit(43)
		}
	default:
		t.Fatal("unknown qualification mode")
	}
	fmt.Fprintln(os.Stdout, "ready")
	if b, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil || b != 'X' {
		t.Fatal("qualification child did not receive the barrier token")
	}
	if mode == "interrupted" {
		os.Exit(23) // Deliberately no COMMIT, rollback or Close.
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func qualificationBaseline() Record {
	return Record{Receipt: time.Unix(1700000000, 0).UTC(), Target: "synthetic", Gauge: "count", Value: 1}
}

func assertQualificationBaseline(t *testing.T, dir string) {
	t.Helper()
	s, err := OpenPersistent(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Read(context.Background(), 10)
	if err != nil || len(r) != 1 || r[0] != qualificationBaseline() {
		t.Fatal("did not reopen with exact acknowledged baseline")
	}
}

func qualificationBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("failed to capture synthetic fixture bytes", err)
	}
	return b
}

type qualificationChild struct {
	finish func(*testing.T)
}

func startQualificationChild(t *testing.T, dir, mode string) qualificationChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQualificationChild$", "-test.timeout=5s", "--", dir, mode)
	cmd.Env = []string{"HOME=/fixtures", "TMPDIR=/fixtures", "LANG=C", "TZ=UTC"}
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		qualificationProbe(t, "CHILD_PIPE_FAILED")
		t.Fatal("could not create child stdin pipe")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = in.Close()
		qualificationProbe(t, "CHILD_PIPE_FAILED")
		t.Fatal("could not create child stdout pipe")
	}
	if err := cmd.Start(); err != nil {
		cancel()
		qualificationProbe(t, "CHILD_START_FAILED")
		t.Fatal("synthetic child did not start")
	}
	qualificationProbe(t, "CHILD_STARTED")
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
	})
	var ready [6]byte
	if _, err := io.ReadFull(out, ready[:]); err != nil {
		waitErr := cmd.Wait()
		waited = true
		phase := "CHILD_BARRIER_FAILED"
		var exit *exec.ExitError
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			phase = "CHILD_TIMEOUT"
		} else if errors.As(waitErr, &exit) {
			switch exit.ExitCode() {
			case 40:
				phase = "CHILD_SQLITE_OPEN_FAILED"
			case 41:
				phase = "CHILD_SQLITE_APPEND_FAILED"
			case 42:
				phase = "CHILD_SQLITE_BEGIN_FAILED"
			case 43:
				phase = "CHILD_SQLITE_UPDATE_FAILED"
			}
		}
		qualificationProbe(t, phase)
		t.Fatal("synthetic child did not reach barrier")
	}
	if string(ready[:]) != "ready\n" {
		qualificationProbe(t, "CHILD_BARRIER_FAILED")
		t.Fatal("child did not reach the expected barrier")
	}
	qualificationProbe(t, "CHILD_READY")
	return qualificationChild{finish: func(t *testing.T) {
		t.Helper()
		if _, err := in.Write([]byte{'X'}); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				qualificationProbe(t, "CHILD_TIMEOUT")
			} else {
				qualificationProbe(t, "CHILD_SIGNAL_FAILED")
			}
			t.Fatal("could not send barrier token")
		}
		_ = in.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(out, 4096))
		err := cmd.Wait()
		waited = true
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			qualificationProbe(t, "CHILD_TIMEOUT")
			t.Fatal("synthetic child exceeded its deadline")
		}
		if mode == "owner" && err != nil {
			qualificationProbe(t, "CHILD_UNEXPECTED_EXIT")
			t.Fatal("synthetic owner did not close as expected")
		}
		if mode == "interrupted" {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				qualificationProbe(t, "CHILD_UNEXPECTED_EXIT")
				t.Fatal("child did not exit at the pre-COMMIT barrier")
			}
		}
		qualificationProbe(t, "CHILD_EXPECTED_EXIT")
	}}
}

func qualificationProbe(t *testing.T, phase string) {
	fmt.Println("QUALPROBE", t.Name(), phase)
}
