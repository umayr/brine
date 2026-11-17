//go:build linux

package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
