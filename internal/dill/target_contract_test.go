package dill

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/target"
)

func TestAlphaExecutionCannotBypassThroughStrictActions(t *testing.T) {
	r := New("/must-not-run")
	r.Profile = target.Alpha
	// Nil source/strict implementation must not be consulted or panic.
	r.CheckSource = func(string) error { t.Fatal("source consulted before Alpha boundary"); return nil }
	ctx := context.Background()
	for _, f := range []func() error{
		func() error { return r.Up(ctx, "/synthetic/recipe", io.Discard) },
		func() error { return r.Start(ctx, "/synthetic/recipe", io.Discard) },
		func() error { return r.Restart(ctx, "/synthetic/recipe", io.Discard) },
		func() error { return r.Teardown(ctx, "/synthetic/recipe", true, io.Discard) },
		func() error { _, err := r.Plan(ctx, "/synthetic/recipe"); return err },
	} {
		if err := f(); !errors.Is(err, target.ErrNegotiationUnavailable) {
			t.Fatalf("contract not enforced: %v", err)
		}
	}
}

func TestPinnedCleanupDeadlineFailsClosed(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "called")
	binary := filepath.Join(root, "dill")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(binary)
	r.Profile = target.Alpha
	r.Engine = "docker"
	r.Socket = "/synthetic/selected.sock"
	r.ResolveStack = func(string) (string, error) { return "demo", nil }
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, action := range []func() error{func() error { return r.Stop(ctx, "/missing/recipe", io.Discard) }, func() error { return r.Down(ctx, "/missing/recipe", io.Discard) }} {
		if err := action(); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline masked as cleanup success: %v", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("expired cleanup launched subprocess")
	}
}

func TestLegacyValidationPinnedAndUnsupportedFailsClosed(t *testing.T) {
	for _, kind := range []string{"docker", "podman"} {
		for _, unsupported := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{true: "unsupported", false: "supported"}[unsupported], func(t *testing.T) {
				root := t.TempDir()
				log := filepath.Join(root, "calls")
				bin := filepath.Join(root, "dill")
				script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n"
				if unsupported {
					script += "exit 2\n"
				} else {
					script += "cat >/dev/null\necho '{\"valid\":true}'\n"
				}
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				r := New(bin)
				r.Engine = kind
				r.Socket = "/synthetic/selected.sock"
				r.ResolveStack = func(string) (string, error) { return "trusted", nil }
				for _, f := range []func() error{func() error { return r.Validate(context.Background(), "/synthetic/recipe") }, func() error { return r.ValidateContent(context.Background(), "/synthetic/recipe", "draft") }} {
					err := f()
					if unsupported != (err != nil) {
						t.Fatalf("unsupported=%v err=%v", unsupported, err)
					}
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) != 2 {
					t.Fatalf("fallback/retry detected: %q", data)
				}
				for _, line := range lines {
					if !strings.Contains(line, "-p trusted --engine "+kind+" --engine-socket /synthetic/selected.sock validate") {
						t.Fatalf("unpinned validation: %s", line)
					}
				}
			})
		}
	}
}
