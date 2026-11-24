package dill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEditorCaptureOverflow(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "fake-dill")
			body := "#!/bin/sh\ndd if=/dev/zero bs=65536 count=65 2>/dev/null\n"
			if stream == "stderr" {
				body = "#!/bin/sh\n(dd if=/dev/zero bs=65536 count=2 2>/dev/null) >&2\n"
			}
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			r := New(script)
			r.Engine = "docker"
			r.ResolveStack = func(string) (string, error) { return "synthetic", nil }
			r.Socket = filepath.Join(dir, "nonexistent-synthetic.sock")
			path := filepath.Join(dir, "stack.pkl")
			if err := r.ValidateContent(context.Background(), path, ""); !errors.Is(err, errCaptureLimit) {
				t.Fatalf("validate: %v", err)
			}
			out, err := r.Fmt(context.Background(), path, "")
			if !errors.Is(err, errCaptureLimit) || out != "" {
				t.Fatalf("fmt: output length %d, %v", len(out), err)
			}
		})
	}
}
