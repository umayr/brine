//go:build linux

package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pipelinePreviewObserver struct {
	before   func()
	terminal func(PreflightInvocation) error
}

func (o pipelinePreviewObserver) Started() error                       { o.before(); return nil }
func (o pipelinePreviewObserver) Terminal(v PreflightInvocation) error { return o.terminal(v) }

func pipelineFixture(t *testing.T, name string, dst any) {
	t.Helper()
	b, e := os.ReadFile("testdata/preflight-v1/" + name + ".json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Stdout json.RawMessage `json:"stdout"`
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(f.Stdout, dst); e != nil {
		t.Fatal(e)
	}
}
func pipelineLines(t *testing.T, path string) []string {
	t.Helper()
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		t.Fatal(e)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
func pipelineSetup(t *testing.T, action, mode string) (*Runner, PreflightBinding, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	r := New(filepath.Join(dir, "fake"))
	r.Engine = "docker"
	r.Socket = "/synthetic/pinned.sock"
	r.ResolveStack = func(string) (string, error) { return "synthetic", nil }
	r.CheckSource = func(string) error { return nil }
	r.BeforeLegacyAction = func(context.Context, string) error { return nil }
	r.QualifiedOperation = &QualifiedOperationConfig{StateDir: state, Layout: "native-local-docker-v1", DaemonID: "independent-daemon"}
	endpoint := "unix:///synthetic/pinned.sock"
	binding := PreflightBinding{Stack: "synthetic", Operation: action, Engine: "docker", EndpointDigest: operationDigest(endpoint), QualifiedIdentityDigest: operationDigest("dill-local-docker-v1\x00independent-daemon")}
	fixture := "preview-up-create"
	switch action {
	case "start", "restart":
		fixture = "preview-empty-start"
	case "stop", "down":
		fixture = "preview-down-absent"
	}
	var preview PreflightResult
	pipelineFixture(t, fixture, &preview)
	preview.Operation = action
	preview.Target = &PreflightTarget{Engine: "docker", EndpointDigest: binding.EndpointDigest, QualifiedIdentityDigest: binding.QualifiedIdentityDigest}
	var caps PreflightCapabilities
	pipelineFixture(t, "capabilities-supported", &caps)
	receipt := consumerReceipt(action)
	receipt.Stack = "synthetic"
	receipt.Target = &OperationTarget{Engine: "docker", EndpointDigest: binding.EndpointDigest, QualifiedIdentityDigest: binding.QualifiedIdentityDigest}
	if mode == "mismatch" {
		receipt.Outcome = "failed"
		receipt.Errors = []OperationDiagnostic{{Code: "PREFLIGHT_MISMATCH", Message: "Reviewed resource selection changed. Review again before retrying."}}
	}
	save := func(name string, b []byte) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	save("operation-caps", consumerJSON(t, json.RawMessage(bytes.TrimSpace(consumerDiscovery(true)))))
	save("preflight-caps", consumerJSON(t, caps))
	save("preview", consumerJSON(t, preview))
	save("receipt", consumerJSON(t, receipt))
	if mode == "unsupported" {
		save("preflight-caps", []byte(`{"version":"dill.preflight-capabilities/1","supported":false}`))
	}
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$1\" in\noperation-capabilities) exec /bin/cat '" + filepath.Join(dir, "operation-caps") + "';;\npreflight-capabilities) exec /bin/cat '" + filepath.Join(dir, "preflight-caps") + "';;\npreflight) "
	switch mode {
	case "overflow":
		script += "exec /usr/bin/yes X"
	case "cancel":
		script += "exec /bin/sleep 3"
	case "exit-disagree":
		script += "/bin/cat '" + filepath.Join(dir, "preview") + "'; exit 1"
	default:
		script += "exec /bin/cat '" + filepath.Join(dir, "preview") + "'"
	}
	script += ";;\noperation) /bin/cat '" + filepath.Join(dir, "receipt") + "'; "
	if mode == "mismatch" {
		script += "exit 1"
	} else {
		script += "exit 0"
	}
	script += ";;\n*) exit 97;;\nesac\n"
	if e := os.WriteFile(r.Binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	return r, binding, log
}
func TestLinuxSyntheticPreflightFullPipeline(t *testing.T) {
	for _, action := range []string{"up", "start", "restart", "stop", "down"} {
		t.Run(action, func(t *testing.T) {
			r, binding, log := pipelineSetup(t, action, "")
			path := "/synthetic/export.pkl"
			previewPersisted := false
			observer := pipelinePreviewObserver{before: func() {
				if n := len(pipelineLines(t, log)); n != 0 {
					t.Fatalf("child before preview intent: %d", n)
				}
			}, terminal: func(v PreflightInvocation) error {
				if v.Result == nil || v.Status != "succeeded" {
					t.Error("invalid preview terminal")
				}
				previewPersisted = true
				return nil
			}}
			preview, e := r.PreviewPreflight(context.Background(), path, action, false, binding, observer)
			if e != nil || !previewPersisted || preview.ProcessExit == nil || *preview.ProcessExit != 0 || preview.Result == nil || preview.Status != "succeeded" {
				t.Fatalf("preview %+v %v", preview, e)
			}
			lines := pipelineLines(t, log)
			if len(lines) != 3 || lines[0] != "operation-capabilities --format json" || lines[1] != "preflight-capabilities --format json" {
				t.Fatalf("preview calls %v", lines)
			}
			want := []string{"preflight", action, "--contract", "dill.preflight/1", "--format", "json", "--engine", "docker", "--engine-socket", "unix:///synthetic/pinned.sock", "-p", "synthetic", "-f", path, "--timeout", "5m", "--coordination-layout", "native-local-docker-v1", "--coordination-daemon-id", "independent-daemon", "--coordination-state-dir", r.QualifiedOperation.StateDir, "--coordination-source", "brine"}
			if action == "up" {
				want = append(want, "--no-prune", "--pull", "never")
			}
			if !reflect.DeepEqual(strings.Fields(lines[2]), want) {
				t.Fatalf("preview argv %s", lines[2])
			}
			persisted := false
			calls := 0
			var out bytes.Buffer
			opObserver := testObserver{start: func() error {
				if len(pipelineLines(t, log)) != 3 {
					t.Error("child before apply intent")
				}
				return nil
			}, finish: func(v OperationInvocation) error {
				if v.Receipt == nil || v.ExitCode != 0 || v.Status != "succeeded" {
					t.Error("invalid operation terminal")
				}
				persisted = true
				return nil
			}}
			v, e := r.ApplyBoundPreflight(WithOperationObserver(context.Background(), opObserver), path, &preview, binding, false, observerWriter{t: t, terminal: &persisted, calls: &calls, output: &out})
			if e != nil || v.Receipt == nil || v.ExitCode != 0 || v.Status != "succeeded" || !persisted || calls != 1 {
				t.Fatalf("apply %+v %v", v, e)
			}
			lines = pipelineLines(t, log)
			if len(lines) != 6 || lines[3] != "operation-capabilities --format json" || lines[4] != "preflight-capabilities --format json" {
				t.Fatalf("apply calls %v", lines)
			}
			want[0] = "operation"
			want[3] = "dill.operation/1"
			want = append(want, "--preflight-contract", "dill.preflight/1", "--expect-preflight", *preview.Result.SelectionDigest)
			if !reflect.DeepEqual(strings.Fields(lines[5]), want) {
				t.Fatalf("apply argv %s", lines[5])
			}
			if _, e = DecodeBoundOperationInvocation([]byte(strings.TrimSpace(strings.TrimPrefix(out.String(), OperationMarker)))); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestLinuxSyntheticPreflightFaults(t *testing.T) {
	for _, mode := range []string{"unsupported", "overflow", "cancel", "exit-disagree", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			r, binding, log := pipelineSetup(t, "up", mode)
			ctx := context.Background()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			observer := pipelinePreviewObserver{before: func() {
				if len(pipelineLines(t, log)) != 0 {
					t.Error("child before intent")
				}
			}, terminal: func(PreflightInvocation) error { return nil }}
			preview, e := r.PreviewPreflight(ctx, "/synthetic/export.pkl", "up", false, binding, observer)
			if mode != "mismatch" {
				if e == nil || preview.Status != "unknown" || preview.Result != nil {
					t.Fatalf("bad preview %+v %v", preview, e)
				}
				n := len(pipelineLines(t, log))
				want := 3
				if mode == "unsupported" {
					want = 2
				}
				if n != want {
					t.Fatalf("fallback or replay: %v", pipelineLines(t, log))
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			persisted := false
			calls := 0
			op := testObserver{start: func() error { return nil }, finish: func(OperationInvocation) error { persisted = true; return nil }}
			v, e := r.ApplyBoundPreflight(WithOperationObserver(ctx, op), "/synthetic/export.pkl", &preview, binding, false, observerWriter{t: t, terminal: &persisted, calls: &calls})
			if e == nil || v.Receipt == nil || v.Receipt.Outcome != "failed" || v.Receipt.Effects != "none" || v.ExitCode != 1 || v.Status != "failed" || !persisted || calls != 1 || len(pipelineLines(t, log)) != 6 {
				t.Fatalf("mismatch %+v %v", v, e)
			}
		})
	}
	t.Run("observer-failures", func(t *testing.T) {
		r, binding, log := pipelineSetup(t, "up", "")
		p := pipelinePreviewObserver{before: func() {}, terminal: func(PreflightInvocation) error { return errors.New("history") }}
		v, e := r.PreviewPreflight(context.Background(), "/synthetic/export.pkl", "up", false, binding, p)
		var he *PreflightHistoryError
		if !errors.As(e, &he) || v.Result == nil {
			t.Fatalf("preview history %v", e)
		}
		calls := 0
		persisted := false
		op := testObserver{start: func() error { return nil }, finish: func(OperationInvocation) error { return errors.New("history") }}
		_, e = r.ApplyBoundPreflight(WithOperationObserver(context.Background(), op), "/synthetic/export.pkl", &v, binding, false, observerWriter{t: t, terminal: &persisted, calls: &calls})
		var oe *OperationHistoryError
		if !errors.As(e, &oe) || calls != 0 || len(pipelineLines(t, log)) != 6 {
			t.Fatalf("apply history %v %d", e, calls)
		}
	})
}
