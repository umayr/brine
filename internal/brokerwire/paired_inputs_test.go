package brokerwire

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBrokerPairedInputsReachNativeRequirements(t *testing.T) {
	binary := os.Getenv("BRINE_TEST_DILL_BROKER_BINARY")
	if binary == "" {
		t.Skip("set native integration Dill binary for consumer check")
	}
	dir := t.TempDir()
	pkl := filepath.Join(dir, "synthetic-pkl")
	script := `#!/bin/sh
printf '{"config":{"name":"%s","engine":"docker","secrets":[{"name":"password"}],"services":{"web":{"image":"alpine","secrets":[{"name":"password","target":"/run/secrets/password","mode":256}]}}}}' "${BRINE_VAR_DOMAIN-unset}"
`
	if err := os.WriteFile(pkl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "compose.pkl")
	if err := os.WriteFile(source, []byte("synthetic source"), 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func() (Requirements, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "broker-requirements", "--contract", Version, "-f", source, "-p", "app")
		cmd.Env = []string{"DILL_PKL_PATH=" + pkl, "BRINE_VAR_DOMAIN=ambient"}
		var out, diagnostics bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diagnostics
		if err := cmd.Run(); err != nil {
			return Requirements{}, err
		}
		var r Requirements
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			return r, err
		}
		if diagnostics.Len() != 0 || !r.Valid() {
			t.Fatal("invalid private requirements output")
		}
		return r, nil
	}
	missing, err := invoke()
	if err != nil {
		t.Fatal(err)
	}
	write := func(value string) {
		raw, err := EncodeInputs(map[string]string{"BRINE_VAR_DOMAIN": value})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, InputsFile), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("captured")
	captured, err := invoke()
	if err != nil || captured.ConfigDigest == missing.ConfigDigest {
		t.Fatal("captured input not bound to config", err)
	}
	write("changed")
	changed, err := invoke()
	if err != nil || changed.ConfigDigest == captured.ConfigDigest {
		t.Fatal("input change not config-bound", err)
	}
	if err = os.WriteFile(filepath.Join(dir, InputsFile), []byte(`{"contract":"dill.broker-inputs/1","variables":{"PATH":"override"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke(); err == nil {
		t.Fatal("target environment override accepted")
	}
}
