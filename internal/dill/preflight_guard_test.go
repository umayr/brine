package dill

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type preflightStrictStub struct{}

func (preflightStrictStub) Up(context.Context, string) error                  { return nil }
func (preflightStrictStub) Plan(context.Context, string) (*PlanResult, error) { return nil, nil }
func (preflightStrictStub) Start(context.Context, string, bool) error         { return nil }
func (preflightStrictStub) Validate(context.Context, string, *string) error   { return nil }

func TestPreflightMandatoryGuards(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, source := range []bool{false, true} {
			for _, action := range []bool{false, true} {
				name := fmtGuardCase(strict, source, action)
				t.Run(name, func(t *testing.T) {
					marker := filepath.Join(t.TempDir(), "child")
					fake := filepath.Join(t.TempDir(), "fake")
					if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
						t.Fatal(err)
					}
					r := New(fake)
					if strict {
						r.StrictActions = preflightStrictStub{}
					}
					if source {
						r.CheckSource = func(string) error { return nil }
					}
					if action {
						r.BeforeLegacyAction = func(context.Context, string) error { return nil }
					}
					err := r.checkPreflightGuards()
					if (err == nil) != (source && action) {
						t.Fatalf("guard = %v", err)
					}
					if source && action {
						return
					}
					preview := &previewTestObserver{}
					result, err := r.PreviewPreflight(context.Background(), "synthetic", "up", false, PreflightBinding{}, preview)
					if err == nil || result.Status != "unknown" || len(preview.events) != 2 || preview.events[0] != "started" || preview.events[1] != "terminal" {
						t.Fatalf("preview %v %+v %v", err, result, preview.events)
					}
					persisted := false
					started := false
					calls := 0
					observer := testObserver{start: func() error { started = true; return nil }, finish: func(OperationInvocation) error {
						if !started {
							t.Error("terminal before intent")
						}
						persisted = true
						return nil
					}}
					var output bytes.Buffer
					w := observerWriter{t: t, terminal: &persisted, calls: &calls, output: &output}
					invocation, err := r.ApplyBoundPreflight(WithOperationObserver(context.Background(), observer), "synthetic", nil, PreflightBinding{Operation: "up"}, false, w)
					if err == nil || invocation.Status != "unknown" || !persisted || calls != 1 {
						t.Fatalf("apply %v %+v persisted=%v writes=%d", err, invocation, persisted, calls)
					}
					if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("Dill child ran despite missing guard")
					}
					persisted = false
					calls = 0
					bad := testObserver{start: func() error { return nil }, finish: func(OperationInvocation) error { return errors.New("disk") }}
					_, err = r.ApplyBoundPreflight(WithOperationObserver(context.Background(), bad), "synthetic", nil, PreflightBinding{Operation: "up"}, false, w)
					var history *OperationHistoryError
					if !errors.As(err, &history) || calls != 0 {
						t.Fatalf("terminal failure exposed marker: %v writes=%d", err, calls)
					}
				})
			}
		}
	}
}
func fmtGuardCase(strict, source, action bool) string {
	flag := func(b bool) string {
		if b {
			return "set"
		}
		return "nil"
	}
	return "strict-" + flag(strict) + "/source-" + flag(source) + "/action-" + flag(action)
}
