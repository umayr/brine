package dill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanCaptureExitOne(t *testing.T) {
	for _, tc := range []struct {
		name, script    string
		valid, overflow bool
	}{
		{"valid", `printf '{}'; printf 'warning' >&2; exit 1`, true, false},
		{"invalid", `printf 'invalid'; printf 'explain-failure' >&2; exit 1`, false, false},
		{"empty", `printf 'explain-failure' >&2; exit 1`, false, false},
		{"stdout-overflow", `printf 'explain-failure' >&2; head -c 5000000 /dev/zero; exit 1`, false, true},
		{"stderr-overflow", `printf '{}'; printf 'explain-failure' >&2; head -c 70000 /dev/zero >&2; exit 1`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "dill")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			r := New(binary)
			r.Engine = "docker"
			r.Socket = "/tmp/never-contacted.sock"
			r.ResolveStack = func(string) (string, error) { return "synthetic", nil }
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := r.Plan(ctx, "/tmp/synthetic.pkl")
			if tc.valid {
				if err != nil || result == nil {
					t.Fatalf("valid exit1 rejected: %v", err)
				}
				return
			}
			if err == nil || result != nil || !isExitCode(err, 1) || !strings.Contains(err.Error(), "explain-failure") {
				t.Fatalf("failure evidence lost: %v", err)
			}
			if tc.overflow && !errors.Is(err, errCaptureLimit) {
				t.Fatalf("overflow lost: %v", err)
			}
		})
	}
}
