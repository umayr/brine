package dill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type previewTestObserver struct {
	events    []string
	failStart bool
}

func (o *previewTestObserver) Started() error {
	o.events = append(o.events, "started")
	if o.failStart {
		return ErrPreflightContract
	}
	return nil
}
func (o *previewTestObserver) Terminal(PreflightInvocation) error {
	o.events = append(o.events, "terminal")
	return nil
}
func TestPreflightNoChildBeforeIntent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child")
	fake := filepath.Join(dir, "fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(fake)
	o := &previewTestObserver{failStart: true}
	v, err := r.PreviewPreflight(context.Background(), "source", "up", false, PreflightBinding{}, o)
	if err == nil || v.Status != "unknown" || len(o.events) != 2 || o.events[0] != "started" || o.events[1] != "terminal" {
		t.Fatalf("%+v %v %v", v, err, o.events)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("child ran before intent")
	}
}
func TestPreflightCaptureFaults(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		deadline     time.Duration
	}{
		{"overflow", "printf '12345678901234567890'", time.Second},
		{"cancel", "exec sleep 3", 20 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "fake")
			if e := os.WriteFile(f, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tc.deadline)
			defer cancel()
			b, _, e := New(f).operationCapture(ctx, 8, "preflight")
			if e == nil || b != nil {
				t.Fatalf("fault accepted %q %v", b, e)
			}
		})
	}
}
