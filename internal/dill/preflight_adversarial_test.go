package dill

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPreflightAdditionalCoherence(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missingTarget":        func(m map[string]any) { m["target"] = nil },
		"emptyQualifiedDigest": func(m map[string]any) { m["target"].(map[string]any)["qualifiedIdentityDigest"] = "" },
		"selectorOnly":         func(m map[string]any) { m["target"].(map[string]any)["qualifiedIdentityDigest"] = nil },
		"badTargetEngine":      func(m map[string]any) { m["target"].(map[string]any)["engine"] = "podman" },
		"badOperationID":       func(m map[string]any) { m["operationId"] = "op_short" },
		"badOperation":         func(m map[string]any) { m["operation"] = "rm" },
		"badStack":             func(m map[string]any) { m["stack"] = "invalid stack" },
		"unknownOutcome":       func(m map[string]any) { m["outcome"] = "changes" },
		"engineWrites":         func(m map[string]any) { m["engineEffects"] = "complete" },
		"noCoordination":       func(m map[string]any) { m["coordinationEffects"] = "none" },
		"uppercaseDigest":      func(m map[string]any) { m["selectionDigest"] = "sha256:" + strings.Repeat("A", 64) },
		"doublePrefixDigest":   func(m map[string]any) { m["selectionDigest"] = "sha256:sha256:" + strings.Repeat("a", 64) },
		"wrongScope":           func(m map[string]any) { preflightScope(m, "network")["scope"] = "all-networks" },
		"incompleteScope":      func(m map[string]any) { preflightScope(m, "volumes")["complete"] = false },
		"noNetwork": func(m map[string]any) {
			m["resources"] = []any{preflightRow(m, 0), preflightRow(m, 2)}
			preflightScope(m, "network")["count"] = 0
		},
		"twoNetworks": func(m map[string]any) {
			n := map[string]any{}
			for k, v := range preflightRow(m, 1) {
				n[k] = v
			}
			n["name"] = "other"
			n["identity"] = "other"
			m["resources"] = append(preflightRows(m), n)
			preflightScope(m, "network")["count"] = 2
		},
		"volumeEnsureWithoutConsumer": func(m map[string]any) {
			m["resources"] = []any{preflightRow(m, 1), preflightRow(m, 2)}
			preflightScope(m, "containers")["count"] = 0
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) { m := preflightBase(t); change(m); preflightReject(t, preflightJSON(t, m)) })
	}
	f := preflightFixtureRead(t, "preview-limit-failure.json")
	for _, field := range []string{"inputDigest", "selectionDigest", "resources", "coverage", "resourcesTruncated", "errors"} {
		t.Run("failure/"+field, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal(f.Stdout, &m)
			switch field {
			case "inputDigest", "selectionDigest":
				m[field] = "sha256:" + strings.Repeat("a", 64)
			case "resources":
				m[field] = preflightBase(t)[field]
			case "coverage":
				m[field] = preflightBase(t)[field]
			case "resourcesTruncated":
				m[field] = false
			case "errors":
				m[field] = []any{}
			}
			preflightReject(t, preflightJSON(t, m))
		})
	}
	for _, code := range []string{"PREFLIGHT_MISMATCH", "SDK_SENTINEL", "CONFIG_INVALID"} {
		var m map[string]any
		_ = json.Unmarshal(f.Stdout, &m)
		m["errors"] = []any{map[string]any{"code": code, "message": "sentinel /private/path"}}
		preflightReject(t, preflightJSON(t, m))
	}
	// Success/failure do not get conflated when subprocess exit disagrees.
	for _, name := range []string{"preview-limit-failure.json", "preview-invalid-contract.json", "preview-metadata-unknown.json"} {
		f := preflightFixtureRead(t, name)
		for _, n := range []int{0, 1, 2} {
			if n == f.ExitCode {
				continue
			}
			v := ObservePreflightInvocation(f.Stdout, &n)
			if v.Result != nil || v.Status != "unknown" {
				t.Fatal("failure exit mismatch")
			}
		}
	}
	// Mutated local invocation DTOs are strictly rejected, not relabeled.
	zero := 0
	v := ObservePreflightInvocation(preflightFixtureRead(t, "preview-up-create.json").Stdout, &zero)
	for _, bad := range []map[string]any{
		{"result": v.Result, "processExit": nil, "status": "succeeded"},
		{"result": v.Result, "processExit": 1, "status": "succeeded"},
		{"result": v.Result, "processExit": 0, "status": "failed"},
		{"result": nil, "processExit": 256, "status": "unknown"},
		{"result": nil, "processExit": 0, "status": "unknown", "extra": true},
	} {
		if _, e := DecodePreflightInvocation(preflightJSON(t, bad)); e != ErrPreflightContract {
			t.Fatal("invalid local invocation")
		}
	}
}

func TestPreflightCapabilityAdversarial(t *testing.T) {
	f := preflightFixtureRead(t, "capabilities-supported.json")
	baseline := f.Stdout
	reject := func(t *testing.T, b []byte) {
		t.Helper()
		if _, e := DecodePreflightCapabilities(baseline); e != nil {
			t.Fatal("invalid baseline")
		}
		if c, e := DecodePreflightCapabilities(b); e != ErrPreflightContract || c != nil {
			t.Fatal("accepted caps")
		}
	}
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["version"] = "dill.operation/1" },
		func(m map[string]any) { m["supported"] = "true" },
		func(m map[string]any) { m["supported"] = false },
		func(m map[string]any) { m["contract"] = "dill.preflight/2" },
		func(m map[string]any) { m["applyContract"] = "dill.operation/2" },
		func(m map[string]any) { m["operations"] = []any{"up", "up", "restart", "stop", "down"} },
		func(m map[string]any) { m["engines"] = []any{"podman"} },
		func(m map[string]any) { m["layouts"] = []any{"darwin"} },
		func(m map[string]any) { m["limits"].(map[string]any)["maxResources"] = 257 },
		func(m map[string]any) { m["limits"].(map[string]any)["maxNameBytes"] = 255.5 },
		func(m map[string]any) { m["limits"].(map[string]any)["maxOutputBytes"] = "262144" },
		func(m map[string]any) { m["limits"].(map[string]any)["unknown"] = true },
		func(m map[string]any) { delete(m["limits"].(map[string]any), "maxResources") },
	} {
		var m map[string]any
		_ = json.Unmarshal(baseline, &m)
		change(m)
		reject(t, preflightJSON(t, m))
	}
	compact := preflightJSON(t, json.RawMessage(baseline))
	for _, b := range [][]byte{
		append([]byte(`{"supported":true,`), compact[1:]...),
		append(compact, []byte(`{}`)...),
		append([]byte("\n"), compact...),
		bytes.Replace(compact, []byte(`"maxResources":256`), []byte(`"maxResources":256,"maxResources":256`), 1),
		bytes.Replace(compact, []byte(`"maxNameBytes":255`), []byte(`"maxNameBytes":null`), 1),
		[]byte(`{"version":"dill.preflight-capabilities/1","supported":null}`),
	} {
		reject(t, b)
	}
}

func TestPreflightSyntheticInputCommitmentSeparation(t *testing.T) {
	b, e := os.ReadFile("testdata/preflight-v1/canonical-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Input struct {
			Payload  json.RawMessage
			Expected string
		}
	}
	if json.Unmarshal(b, &f) != nil {
		t.Fatal("fixture")
	}
	digest := func(m map[string]any) string {
		sum := sha256.Sum256(append([]byte("dill.effective-input/1\x00"), preflightJSON(t, m)...))
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	for _, field := range []string{"desiredConfigHash", "preparedImageId", "dependsOn", "flags", "services"} {
		// UseNumber avoids float conversion of the canonical timeout integer.
		d := json.NewDecoder(bytes.NewReader(f.Input.Payload))
		d.UseNumber()
		var m map[string]any
		if d.Decode(&m) != nil {
			t.Fatal("payload")
		}
		if digest(m) != f.Input.Expected {
			t.Fatal("invalid baseline")
		}
		service := m["services"].([]any)[0].(map[string]any)
		switch field {
		case "desiredConfigHash":
			service[field] = strings.Repeat("9", 64)
		case "preparedImageId":
			service[field] = "sha256:" + strings.Repeat("9", 64)
		case "dependsOn":
			service[field] = []any{"synthetic-db"}
		case "flags":
			m[field].(map[string]any)["forceRecreate"] = true
		case "services":
			m[field] = []any{}
		}
		if digest(m) == f.Input.Expected {
			t.Fatal("lost commitment", field)
		}
	}
}

func FuzzPreflightDecode(f *testing.F) {
	for _, name := range []string{"preview-up-create.json", "preview-up-recreate.json", "preview-empty-start.json", "preview-metadata-unknown.json", "preview-limit-failure.json", "preview-invalid-contract.json"} {
		b, e := os.ReadFile("testdata/preflight-v1/" + name)
		if e != nil {
			f.Fatal(e)
		}
		var v preflightFixture
		if json.Unmarshal(b, &v) != nil {
			f.Fatal("fixture")
		}
		f.Add([]byte(v.Stdout))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, e := DecodePreflightResult(b)
		if e != nil {
			if e != ErrPreflightContract || r != nil {
				t.Fatal("unsafe error")
			}
			return
		}
		n := 1
		if r.Outcome == "succeeded" {
			n = 0
		} else if r.Outcome == "invalid_input" {
			n = 2
		}
		v := ObservePreflightInvocation(b, &n)
		if v.Result == nil {
			t.Fatal("decode observation disagrees")
		}
		if _, e := DecodePreflightInvocation(preflightJSON(t, v)); e != nil {
			t.Fatal("round trip", e)
		}
	})
}
