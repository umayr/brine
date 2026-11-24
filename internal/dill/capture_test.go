package dill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureStreams(t *testing.T) {
	tests := []struct {
		name, script, want string
		fail               bool
	}{
		{"warning", "printf 'result'; printf 'warning' >&2", "result", false},
		{"stdout overflow", "head -c 16777216 /dev/zero", "", true},
		{"stderr overflow", "head -c 16777216 /dev/zero >&2", "", true},
		{"failure", "printf 'diagnostic' >&2; exit 7", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "dill")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+tt.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := New(binary).capture(ctx, "test")
			if ctx.Err() != nil {
				t.Fatalf("capture did not drain: %v", ctx.Err())
			}
			if (err != nil) != tt.fail {
				t.Fatalf("error = %v, want failure %v", err, tt.fail)
			}
			if !tt.fail && got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
			if tt.name == "failure" && (!isExitCode(err, 7) || !strings.Contains(err.Error(), "diagnostic") || got != "") {
				t.Fatalf("failure diagnostics/exit lost: %q %v", got, err)
			}
			if strings.Contains(tt.name, "overflow") && (got != "" || !strings.Contains(err.Error(), "capture limit")) {
				t.Fatalf("overflow accepted/truncated: %q %v", got, err)
			}
		})
	}
}
