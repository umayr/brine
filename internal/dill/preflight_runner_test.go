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

func TestBoundDecoderExplicitOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/preflight-v1/apply-mismatch.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ExitCode int             `json:"exitCode"`
		Stdout   json.RawMessage `json:"stdout"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var receipt OperationReceipt
	if err = json.Unmarshal(fixture.Stdout, &receipt); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(OperationInvocation{Receipt: &receipt, ExitCode: fixture.ExitCode, Status: "failed"})
	if _, err = DecodeOperationInvocation(payload); err == nil {
		t.Fatal("old decoder accepted bound diagnostic")
	}
	if _, err = DecodeBoundOperationInvocation(payload); err != nil {
		t.Fatal(err)
	}
	receipt.Errors[0] = OperationDiagnostic{Code: "PREFLIGHT_UNAVAILABLE", Message: "Complete preflight evidence unavailable."}
	payload, _ = json.Marshal(OperationInvocation{Receipt: &receipt, ExitCode: 1, Status: "failed"})
	if _, err = DecodeOperationInvocation(payload); err == nil {
		t.Fatal("old decoder accepted unavailable")
	}
	if _, err = DecodeBoundOperationInvocation(payload); err != nil {
		t.Fatal(err)
	}
	receipt.Effects = "complete"
	payload, _ = json.Marshal(OperationInvocation{Receipt: &receipt, ExitCode: 1, Status: "failed"})
	if _, err = DecodeBoundOperationInvocation(payload); err == nil {
		t.Fatal("accepted contradictory effects")
	}
}

func TestPreflightDiscoverySyntheticProcess(t *testing.T) {
	good, err := os.ReadFile("testdata/preflight-v1/capabilities-supported.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Stdout json.RawMessage `json:"stdout"`
	}
	if err = json.Unmarshal(good, &fixture); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, fixture.Stdout); err != nil {
		t.Fatal(err)
	}
	// A synthetic shell only; no Dill, Pkl, engine or socket is used.
	for _, tc := range []struct {
		name, output string
		code         int
		ok           bool
	}{
		{"supported", compact.String() + "\n", 0, true},
		{"bad exit", compact.String() + "\n", 1, false},
		{"extra frame", compact.String() + "\n{}\n", 0, false},
		{"unsupported", `{"version":"dill.preflight-capabilities/1","supported":false}` + "\n", 0, false},
		{"oversize", strings.Repeat("x", 9000), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "fake")
			script := "#!/bin/sh\nprintf '%s' '" + tc.output + "'\nexit " + string(rune('0'+tc.code)) + "\n"
			if err := os.WriteFile(file, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			err := New(file).checkPreflightCapabilities(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
