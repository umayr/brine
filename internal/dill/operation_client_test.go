package dill

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/target"
)

func consumerScript(t *testing.T, discovery, receipt []byte, exit int, extra string) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "dill")
	log := filepath.Join(root, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$1\" in\noperation-capabilities) printf '%s' '" + string(discovery) + "'; exit 0;;\nesac\n" + extra + "\nprintf '%s' '" + string(receipt) + "'\necho PRIVATE_DIAGNOSTIC >&2\nexit " + string(rune('0'+exit)) + "\n"
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	r := New(bin)
	r.Engine = "docker"
	r.Socket = "/synthetic/engine.sock"
	r.ResolveStack = func(string) (string, error) { return "app", nil }
	return r, log
}
func TestOperationProcessCaptureDoesNotLeakOrRetry(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		exit       int
		status     string
	}{
		{"success", "valid", 0, "succeeded"}, {"missing", "missing", 0, "unknown"}, {"malformed", "malformed", 0, "unknown"}, {"unsupported", "unsupported", 2, "unknown"}, {"wrong-target", "wrong-target", 0, "unknown"}, {"cancelled", "cancelled", 1, "cancelled"}, {"declined", "declined", 0, "cancelled"}, {"exit-mismatch", "valid", 2, "unknown"}, {"duplicate", "duplicate", 0, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := consumerReceipt("plan")
			switch test.mode {
			case "wrong-target":
				receipt.Stack = "other"
			case "cancelled":
				receipt.Outcome = "cancelled"
				receipt.Errors = []OperationDiagnostic{{Code: "CANCELLED", Message: operationMessages["CANCELLED"]}}
			case "declined":
				receipt.Outcome = "cancelled"
			}
			data := append(consumerJSON(t, receipt), '\n')
			switch test.mode {
			case "missing":
				data = nil
			case "malformed":
				data = []byte("PRIVATE_STDOUT\n")
			case "unsupported":
				data = []byte("unsupported PRIVATE_STDOUT\n")
			case "duplicate":
				data = bytes.Replace(data, []byte(`"outcome":`), []byte(`"outcome":"succeeded","outcome":`), 1)
			}
			r, log := consumerScript(t, consumerDiscovery(false), data, test.exit, "")
			var out bytes.Buffer
			terminal, e := r.operation(context.Background(), "/synthetic/config.pkl", "plan", &out)
			if terminal.Status != test.status {
				t.Fatalf("status=%s error=%v", terminal.Status, e)
			}
			if (e == nil) != (test.status == "succeeded") {
				t.Fatalf("incorrect overall error: %v", e)
			}
			if strings.Contains(out.String(), "PRIVATE") || (e != nil && strings.Contains(e.Error(), "PRIVATE")) {
				t.Fatal("raw diagnostics leaked")
			}
			if strings.Count(out.String(), OperationMarker) != 1 {
				t.Fatal("terminal not emitted exactly once")
			}
			if _, err := DecodeOperationInvocation([]byte(strings.TrimSuffix(strings.TrimPrefix(out.String(), OperationMarker), "\n"))); err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 2 {
				t.Fatalf("fallback/retry: %s", calls)
			}
			if strings.Contains(string(calls), "--coordination-") {
				t.Fatal("read-only operation coordinated")
			}
		})
	}
}
func TestOperationDiscoveryFailureNeverDispatches(t *testing.T) {
	r, log := consumerScript(t, []byte("UNSUPPORTED PRIVATE\n"), nil, 0, "")
	var out bytes.Buffer
	terminal, e := r.operation(context.Background(), "/synthetic/config.pkl", "plan", &out)
	if e == nil || terminal.Receipt != nil || terminal.Status != "unknown" {
		t.Fatal("discovery accepted")
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "\n") != 1 {
		t.Fatal("operation ran after discovery failed")
	}
}
func TestOperationSubprocessCancellationStartAndOverflow(t *testing.T) {
	for _, mode := range []string{"cancel", "stdout-overflow", "stderr-overflow", "start"} {
		t.Run(mode, func(t *testing.T) {
			extra := ""
			if mode == "cancel" {
				extra = "exec /bin/sleep 30"
			}
			if mode == "stdout-overflow" {
				extra = "/usr/bin/head -c 2200000 /dev/zero; exec /bin/sleep 30"
			}
			if mode == "stderr-overflow" {
				extra = "/usr/bin/head -c 9000 /dev/zero >&2; exec /bin/sleep 30"
			}
			r, _ := consumerScript(t, consumerDiscovery(false), nil, 0, extra)
			if mode == "start" {
				r.Binary = "/must-not-run"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			var out bytes.Buffer
			start := time.Now()
			terminal, e := r.operation(ctx, "/synthetic/config.pkl", "plan", &out)
			if time.Since(start) > 3*time.Second {
				t.Fatal("unbounded subprocess")
			}
			if e == nil || terminal.Receipt != nil || terminal.Status != "unknown" {
				t.Fatal("uncertain invocation accepted")
			}
			if strings.Contains(out.String(), "PRIVATE") {
				t.Fatal("raw leak")
			}
		})
	}
}
func TestOperationConfiguredRoutingCannotDowngrade(t *testing.T) {
	r := New("/must-not-run")
	r.Profile = target.Alpha
	r.Engine = "docker"
	r.Socket = "/synthetic/engine.sock"
	r.ResolveStack = func(string) (string, error) { return "app", nil }
	r.QualifiedOperation = &QualifiedOperationConfig{}
	r.BeforeLegacyAction = func(context.Context, string) error { t.Fatal("invalid config reached legacy/secret guard"); return nil }
	r.CheckSource = func(string) error { t.Fatal("invalid config consulted source"); return nil }
	ctx := context.Background()
	for _, action := range []func(io.Writer) error{func(w io.Writer) error { return r.Up(ctx, "missing", w) }, func(w io.Writer) error { return r.Start(ctx, "missing", w) }, func(w io.Writer) error { return r.Restart(ctx, "missing", w) }, func(w io.Writer) error { return r.Stop(ctx, "missing", w) }, func(w io.Writer) error { return r.Down(ctx, "missing", w) }} {
		var out bytes.Buffer
		e := action(&out)
		var oe *OperationError
		if !errors.As(e, &oe) || oe.Invocation.Receipt != nil || oe.Invocation.Status != "unknown" {
			t.Fatalf("bad routing result: %v", e)
		}
		if strings.Count(out.String(), OperationMarker) != 1 {
			t.Fatal("missing failed terminal")
		}
	}
	if e := r.Teardown(ctx, "missing", true, io.Discard); e == nil {
		t.Fatal("qualified teardown accepted")
	}
	if _, e := r.Plan(ctx, "missing"); e == nil {
		t.Fatal("bad-config plan accepted")
	}
	if e := r.ValidateContent(ctx, "missing", "SECRET"); !errors.Is(e, ErrOperationContract) {
		t.Fatal("stdin fallback")
	}
	if e := r.checkExecutionContract(); !errors.Is(e, target.ErrNegotiationUnavailable) {
		t.Fatal("secret controller bypass")
	}
	r.Profile = target.Legacy
	if e := r.checkExecutionContract(); e == nil {
		t.Fatal("configured legacy secret bypass")
	}
}
func TestOperationConfigurationAndSocketValidation(t *testing.T) {
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	q := &QualifiedOperationConfig{StateDir: state, Layout: "native-local-docker-v1", DaemonID: "independently-provisioned-id"}
	if e := validateQualifiedOperationConfig("linux", "docker", "unix:///synthetic/engine.sock", q); e != nil {
		t.Fatal(e)
	}
	for _, host := range []string{"darwin", "windows"} {
		if validateQualifiedOperationConfig(host, "docker", "/synthetic/sock", q) == nil {
			t.Fatal("nonlinux qualified")
		}
	}
	if runtime.GOOS != "linux" && ValidateQualifiedOperationConfig("docker", "/synthetic/sock", q) == nil {
		t.Fatal("runtime host bypass")
	}
	for _, mode := range []os.FileMode{0755, 0770, 0600} {
		_ = os.Chmod(state, mode)
		if validateQualifiedOperationConfig("linux", "docker", "/synthetic/sock", q) == nil {
			t.Fatal("unsafe state mode accepted")
		}
	}
	_ = os.Chmod(state, 0700)
	link := filepath.Join(t.TempDir(), "state")
	if e := os.Symlink(state, link); e != nil {
		t.Fatal(e)
	}
	copy := *q
	copy.StateDir = link
	if validateQualifiedOperationConfig("linux", "docker", "/synthetic/sock", &copy) == nil {
		t.Fatal("symlink state accepted")
	}
	for _, id := range []string{"short", "contains space", strings.Repeat("x", 129)} {
		copy := *q
		copy.DaemonID = id
		if validateQualifiedOperationConfig("linux", "docker", "/synthetic/sock", &copy) == nil {
			t.Fatal("invalid daemon identity accepted")
		}
	}
	for _, socket := range []string{"relative", "tcp://host/path", "unix://host/path", "unix:/path", "/path?x", "/path#x", "/path%20", "//path", "/path ", "/path\\x", "/path\n"} {
		if _, e := normalizeOperationSocket(socket); e == nil {
			t.Fatalf("invalid socket accepted: %q", socket)
		}
	}
	a, _ := normalizeOperationSocket("/synthetic/engine.sock")
	b, _ := normalizeOperationSocket("unix:///synthetic/engine.sock")
	if a != b {
		t.Fatal("selector normalization mismatch")
	}
}
func TestQualifiedOperationProcessesLinuxOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux host gate; no production bypass")
	}
	for _, op := range []string{"up", "start", "restart", "stop", "down"} {
		t.Run(op, func(t *testing.T) {
			receipt := consumerReceipt(op)
			receipt.Effects = "complete"
			receipt.Target.QualifiedIdentityDigest = operationDigest("dill-local-docker-v1\x00independently-provisioned-id")
			r, log := consumerScript(t, consumerDiscovery(true), append(consumerJSON(t, receipt), '\n'), 0, "")
			state := t.TempDir()
			_ = os.Chmod(state, 0700)
			r.Profile = target.Alpha
			r.QualifiedOperation = &QualifiedOperationConfig{StateDir: state, Layout: "native-local-docker-v1", DaemonID: "independently-provisioned-id"}
			var out bytes.Buffer
			terminal, e := r.operation(context.Background(), "/synthetic/config.pkl", op, &out)
			if e != nil || terminal.Status != "succeeded" {
				t.Fatalf("qualified operation failed: %v %s", e, out.String())
			}
			calls, _ := os.ReadFile(log)
			for _, flag := range []string{"operation " + op + " --contract dill.operation/1", "--coordination-source brine", "--coordination-state-dir " + state, "--coordination-layout native-local-docker-v1", "--coordination-daemon-id independently-provisioned-id"} {
				if !strings.Contains(string(calls), flag) {
					t.Fatalf("missing %s", flag)
				}
			}
			if op == "up" && !strings.Contains(string(calls), "--no-prune --pull never") {
				t.Fatal("unsafe up defaults")
			}
			if strings.Contains(string(calls), "--attended") {
				t.Fatal("invented attended flag")
			}
		})
	}
}
