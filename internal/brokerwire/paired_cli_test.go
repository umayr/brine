package brokerwire

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPairedDillAcceptsPrivateWireBeforePlatformGuard(t *testing.T) {
	binary := os.Getenv("BRINE_TEST_DILL_BROKER_BINARY")
	if binary == "" {
		t.Skip("set BRINE_TEST_DILL_BROKER_BINARY to the native integration build")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("this check relies on the Darwin unsupported-store boundary; it is not Linux runtime qualification")
	}
	scope := brokerScope()
	scope.TargetDigest = Digest([]byte("dill-local-docker-v1\x00daemon-test"))
	scope.EndpointDigest = Digest([]byte("unix:///operator/docker.sock"))
	item, _ := NewItem("password", []byte("synthetic-private-value"))
	e := Envelope{Scope: scope, Items: []Item{item}}
	defer e.Close()
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "broker-prepare", "--contract", Version, "--broker-store", "/operator/store", "-f", "/capture/main.pkl", "-p", "app", "--engine-socket", "/operator/docker.sock", "--coordination-daemon-id", "daemon-test", "--coordination-state-dir", "/operator/coordination", "--source-digest", scope.SourceDigest)
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(raw)
	var out, diagnostics bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	err = cmd.Run()
	exit, ok := err.(*exec.ExitError)
	var reply struct {
		Contract string `json:"contract"`
		Code     string `json:"code"`
	}
	if !ok || exit.ExitCode() != 1 || json.Unmarshal(out.Bytes(), &reply) != nil || reply.Contract != Version || reply.Code != "BROKER_UNSUPPORTED" || diagnostics.Len() != 0 || strings.Contains(out.String(), "synthetic-private") {
		t.Fatal("paired wire/strict-dispatch mismatch")
	}
}
