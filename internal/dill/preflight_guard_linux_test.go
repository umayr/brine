//go:build linux

package dill

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// Linux-only synthetic process regression: a fully valid local config makes the
// old missing-guard path reach discovery. No real Dill, socket, engine or Pkl.
func TestPreflightLinuxMissingGuardsNeverLaunch(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, source := range []bool{false, true} {
			for _, action := range []bool{false, true} {
				if source && action {
					continue
				}
				t.Run(fmtGuardCase(strict, source, action), func(t *testing.T) {
					r, log := consumerScript(t, consumerDiscovery(true), nil, 0, "")
					dir := t.TempDir()
					if err := os.Chmod(dir, 0700); err != nil {
						t.Fatal(err)
					}
					r.QualifiedOperation = &QualifiedOperationConfig{StateDir: dir, Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
					if strict {
						r.StrictActions = preflightStrictStub{}
					}
					if source {
						r.CheckSource = func(string) error { return nil }
					}
					if action {
						r.BeforeLegacyAction = func(context.Context, string) error { return nil }
					}
					endpoint, err := normalizeOperationSocket(r.Socket)
					if err != nil {
						t.Fatal(err)
					}
					binding := PreflightBinding{Stack: "app", Operation: "up", Engine: "docker", EndpointDigest: operationDigest(endpoint), QualifiedIdentityDigest: operationDigest("dill-local-docker-v1\x00" + r.QualifiedOperation.DaemonID)}
					obs := &previewTestObserver{}
					if _, err = r.PreviewPreflight(context.Background(), "synthetic", "up", false, binding, obs); err == nil {
						t.Fatal("preview accepted missing guard")
					}
					if _, err = os.Stat(log); !os.IsNotExist(err) {
						t.Fatal("preview launched child before guards")
					}
					persisted := false
					calls := 0
					observer := testObserver{start: func() error { return nil }, finish: func(OperationInvocation) error { persisted = true; return nil }}
					var out bytes.Buffer
					_, err = r.ApplyBoundPreflight(WithOperationObserver(context.Background(), observer), "synthetic", nil, binding, false, observerWriter{t: t, terminal: &persisted, calls: &calls, output: &out})
					if err == nil || !persisted || calls != 1 {
						t.Fatalf("apply err=%v persisted=%v markers=%d", err, persisted, calls)
					}
					if _, err = os.Stat(log); !os.IsNotExist(err) {
						t.Fatal("apply launched child before guards")
					}
				})
			}
		}
	}
}
