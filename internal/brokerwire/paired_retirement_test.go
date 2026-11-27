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

func TestBrokerPairedRetirementBeforePlatformGuard(t *testing.T) {
	binary := os.Getenv("BRINE_TEST_DILL_BROKER_BINARY")
	if binary == "" || runtime.GOOS != "darwin" {
		t.Skip("native integration binary + Darwin unsupported store boundary")
	}
	scope := brokerScope()
	scope.TargetDigest = Digest([]byte("dill-local-docker-v1\x00daemon-test"))
	scope.EndpointDigest = Digest([]byte("unix:///operator/docker.sock"))
	old := scope
	old.Generation = strings.Repeat("f", 32)
	scope.BindingRevision = old.BindingRevision + 1
	r := Retirement{Contract: RetirementVersion, Scope: scope, Previous: []Reference{{Scope: old, Handle: strings.Repeat("b", 32)}}}
	raw, err := EncodeRetirement(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "broker-retirement-prepare", "--contract", Version, "--broker-store", "/operator/store", "-f", "/capture/main.pkl", "-p", scope.Stack, "--engine-socket", "/operator/docker.sock", "--coordination-daemon-id", "daemon-test", "--coordination-state-dir", "/operator/coordination", "--source-digest", scope.SourceDigest)
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
	if !ok || exit.ExitCode() != 1 || json.Unmarshal(out.Bytes(), &reply) != nil || reply.Contract != Version || reply.Code != "BROKER_UNSUPPORTED" || diagnostics.Len() != 0 {
		t.Fatal("paired retirement metadata/strict dispatch mismatch", out.String(), err)
	}
}
