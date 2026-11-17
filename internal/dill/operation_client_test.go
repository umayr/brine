package dill

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
			_, e := r.operation(context.Background(), "/synthetic/config.pkl", op, &out)
			if e != ErrAlphaDisabled || out.Len() != 0 {
				t.Fatalf("disabled qualified operation: %v %s", e, out.String())
			}
			if _, e := os.Stat(log); !os.IsNotExist(e) {
				t.Fatalf("disabled operation launched child: %v", e)
			}
		})
	}
}
