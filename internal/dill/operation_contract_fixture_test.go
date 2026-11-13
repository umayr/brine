package dill

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const operationCandidateCommit = "b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2"
const operationCandidateSchemaSHA = "b34206d0622fc6c1aa99d5edefb9b6d4b01232635756d2c67fb135161f8db345"

// Independent hashes bind every public source fixture, including the manifest.
// Fixture expected statuses are decoder inputs, NEVER process execution evidence.
var operationFixtureHashes = map[string]string{
	"failure.json":                   "448d2e331c46cf985e05814bebfe285dd293f7bb4fddd0dc5983f6807cfb1f4e",
	"manifest.json":                  "1923e39eb9fedef441f50079f240063144ca3f40c0409cfac57bc04f988d5181",
	"qualified-cleanup-failure.json": "67fd90a487075b76134c3bf6031a17c9ca88a57588a229fbee521f6935217a04",
	"qualified-success.json":         "6c2f603ee6eb6509dd055b2aebf32304c37ee6a7d6b416c2921cd9c47d9ec955",
	"success.json":                   "d8fb618b7c1728ba2a2c03c7836fb47a46d414074f3610aad3a1e13b5d7f6c91",
	"unsupported-operation.json":     "31b22d9377cc397b02ea46a8ff8617a24f8b2fe9e2727f1cf4f96bd359a6c1f1",
	"unsupported-version.json":       "2aaf103b10bbddd2b0b34400ce85f29783b459c6a3e4e0bb5fa5aeea23da943d",
}

type operationPublicFixture struct {
	Runtime      string          `json:"runtime"`
	Args         []string        `json:"args"`
	ExpectedExit int             `json:"expectedExit"`
	Expected     json.RawMessage `json:"expected"`
}

func operationFixture(t *testing.T, name string) operationPublicFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "operation-v1", name))
	if err != nil {
		t.Fatal(err)
	}
	var f operationPublicFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Contract goldens test the real Brine decoder, not a parallel test-only schema.
func TestOperationContractConsumerFixtures(t *testing.T) {
	statuses := map[string]string{"success.json": "succeeded", "failure.json": "failed", "unsupported-operation.json": "failed", "unsupported-version.json": "failed", "qualified-success.json": "succeeded", "qualified-cleanup-failure.json": "failed"}
	for name, status := range statuses {
		t.Run(name, func(t *testing.T) {
			f := operationFixture(t, name)
			receipt, err := parseOperationReceipt(f.Expected)
			if err != nil {
				t.Fatalf("exact b3 fixture rejected: %v", err)
			}
			if got := invocationStatus(receipt, f.ExpectedExit); got != status {
				t.Fatalf("status %s want %s", got, status)
			}
			marker, err := json.Marshal(OperationInvocation{Receipt: receipt, ExitCode: f.ExpectedExit, Status: status})
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeOperationInvocation(marker)
			if err != nil || !reflect.DeepEqual(decoded.Receipt, receipt) {
				t.Fatal("validated terminal lost producer axes")
			}
			if strings.HasPrefix(name, "unsupported-") && (receipt.Target != nil || receipt.Stack != "") {
				t.Fatal("early rejection manufactured target")
			}
			if name == "qualified-cleanup-failure.json" && (decoded.Status != "failed" || decoded.Receipt.Outcome != "succeeded" || decoded.Receipt.Effects != "complete" || decoded.ExitCode != 1) {
				t.Fatal("postcommit cleanup failure became success/unknown")
			}
			if status != "succeeded" {
				forged, _ := json.Marshal(OperationInvocation{Receipt: receipt, ExitCode: f.ExpectedExit, Status: "succeeded"})
				if _, err := DecodeOperationInvocation(forged); err == nil {
					t.Fatal("marker outcome/exit disagreement became success")
				}
			}
			compact := &bytes.Buffer{}
			if json.Compact(compact, f.Expected) != nil {
				t.Fatal("compact")
			}
			valid := append(compact.Bytes(), '\n')
			if !operationLine(valid, maxOperationOutput) {
				t.Fatal("valid process framing rejected")
			}
			for _, malformed := range [][]byte{valid[:len(valid)-1], append(append([]byte{}, valid...), valid...), append(append([]byte{}, valid...), '\n'), append([]byte{0xff}, valid...)} {
				if operationLine(malformed, maxOperationOutput) {
					t.Fatal("unsafe process framing accepted")
				}
			}
		})
	}
	base := operationFixture(t, "qualified-success.json").Expected
	mutations := map[string][]byte{
		"duplicate-top":     bytes.Replace(base, []byte(`"version":`), []byte(`"version":"dill.operation/1","version":`), 1),
		"unknown-top":       bytes.Replace(base, []byte(`"version":`), []byte(`"unreviewed":true,"version":`), 1),
		"duplicate-target":  bytes.Replace(base, []byte(`"engine":`), []byte(`"engine":"docker","engine":`), 1),
		"unknown-target":    bytes.Replace(base, []byte(`"engine":`), []byte(`"attestation":true,"engine":`), 1),
		"null-qualified":    bytes.Replace(base, []byte(`"sha256:e8602058ec84f05ce8c9db6c09ab4163243537579c950a40d0e82d724d784a80"`), []byte(`null`), 1),
		"bad-qualified":     bytes.Replace(base, []byte(`sha256:e8602058`), []byte(`e8602058`), 1),
		"bad-id":            bytes.Replace(base, []byte(`op_00000000000000000000000000000000`), []byte(`op_bad`), 1),
		"unknown-resource":  bytes.Replace(base, []byte(`"action":`), []byte(`"raw":true,"action":`), 1),
		"null-errors":       bytes.Replace(base, []byte(`"errors": []`), []byte(`"errors": null`), 1),
		"missing-field":     bytes.Replace(base, []byte(`"changesDetected": false,`), nil, 1),
		"trailing-document": append(append([]byte{}, base...), []byte(` {}`)...),
	}
	for name, data := range mutations {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(base, data) {
				t.Fatal("mutation did not reach fixture")
			}
			if _, err := parseOperationReceipt(data); err == nil {
				t.Fatal("malformed receipt accepted")
			}
		})
	}
	receipt, err := parseOperationReceipt(base)
	if err != nil {
		t.Fatal(err)
	}
	q := &QualifiedOperationConfig{DaemonID: "synthetic-docker-host-1234"}
	if bindOperationReceipt(receipt, "stop", "app", "docker", "unix:///synthetic/qualified.sock", q) != nil {
		t.Fatal("exact qualified binding rejected")
	}
	for _, mismatch := range []struct {
		op, stack, engine, endpoint string
		q                           *QualifiedOperationConfig
	}{
		{"start", "app", "docker", "unix:///synthetic/qualified.sock", q},
		{"stop", "other", "docker", "unix:///synthetic/qualified.sock", q},
		{"stop", "app", "podman", "unix:///synthetic/qualified.sock", q},
		{"stop", "app", "docker", "unix:///other.sock", q},
		{"stop", "app", "docker", "unix:///synthetic/qualified.sock", &QualifiedOperationConfig{DaemonID: "other-daemon-id"}},
		{"stop", "app", "docker", "unix:///synthetic/qualified.sock", nil},
	} {
		if bindOperationReceipt(receipt, mismatch.op, mismatch.stack, mismatch.engine, mismatch.endpoint, mismatch.q) == nil {
			t.Fatal("mismatched request accepted")
		}
	}
	for name, change := range map[string]func(map[string]any){
		"null-resources": func(m map[string]any) { m["resources"] = nil },
		"resource-budget": func(m map[string]any) {
			r := m["resources"].([]any)[0]
			a := make([]any, 4097)
			for i := range a {
				a[i] = r
			}
			m["resources"] = a
		},
		"error-budget": func(m map[string]any) {
			a := make([]any, 17)
			for i := range a {
				a[i] = map[string]any{"code": "CANCELLED", "message": "Operation cancelled or deadline exceeded."}
			}
			m["errors"] = a
		},
		"unknown-error": func(m map[string]any) { m["errors"] = []any{map[string]any{"code": "FUTURE", "message": "unreviewed"}} },
		"raw-error": func(m map[string]any) {
			m["errors"] = []any{map[string]any{"code": "OPERATION_FAILED", "message": "raw evaluator or SDK private data"}}
		},
		"nonboolean": func(m map[string]any) { m["changesDetected"] = "false" },
	} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if json.Unmarshal(base, &m) != nil {
				t.Fatal("base")
			}
			change(m)
			if _, err := parseOperationReceipt(operationContractJSON(t, m)); err == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
	if _, err := parseOperationReceipt(bytes.Repeat([]byte(" "), maxOperationOutput+1)); err == nil {
		t.Fatal("oversized terminal accepted")
	}
	// Opposite nonzero exits must not turn known axes into overall success.
	for _, name := range []string{"success.json", "qualified-success.json"} {
		f := operationFixture(t, name)
		r, _ := parseOperationReceipt(f.Expected)
		if invocationStatus(r, 1) == "succeeded" {
			t.Fatal("nonzero process exit accepted as success")
		}
	}
}

// Discovery has no upstream public JSON golden. These independent exact b3
// field/list expectations come from cmd/dill/operation.go, not a producer fixture.
func operationContractDiscovery(qualified bool) map[string]any {
	features := []string{"explicitLocalSelector", "attended", "redactedSummary", "conservativeEffects"}
	m := map[string]any{"version": "dill.operation/1", "operations": []string{"validate", "plan", "diff", "up", "down", "stop", "start", "restart", "rm", "teardown"}, "engines": []string{"docker", "podman"}, "features": features}
	if qualified {
		m["features"] = append(features, "optInQualifiedLocalCoordination")
		m["qualifiedCoordinationOperations"] = []string{"up", "down", "stop", "start", "restart", "rm"}
		m["qualifiedCoordinationEngines"] = []string{"docker"}
		m["qualifiedCoordinationLayouts"] = []string{"native-local-docker-v1"}
	}
	return m
}
func operationContractJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func TestOperationContractConsumerCapabilities(t *testing.T) {
	ordinary := operationContractJSON(t, operationContractDiscovery(false))
	qualified := operationContractJSON(t, operationContractDiscovery(true))
	if !validOperationDiscovery(ordinary, false) || validOperationDiscovery(ordinary, true) || !validOperationDiscovery(qualified, true) {
		t.Fatal("b3 discovery negotiation mismatch")
	}
	for _, key := range []string{"qualifiedCoordinationOperations", "qualifiedCoordinationEngines", "qualifiedCoordinationLayouts"} {
		for _, kind := range []string{"missing", "null", "empty", "unknown"} {
			t.Run(key+"/"+kind, func(t *testing.T) {
				m := operationContractDiscovery(true)
				switch kind {
				case "missing":
					delete(m, key)
				case "null":
					m[key] = nil
				case "empty":
					m[key] = []string{}
				case "unknown":
					m[key] = []string{"unreviewed"}
				}
				if validOperationDiscovery(operationContractJSON(t, m), false) || validOperationDiscovery(operationContractJSON(t, m), true) {
					t.Fatal("incomplete qualified advertisement accepted")
				}
			})
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"feature-without-keys": func(m map[string]any) {
			m["features"] = []string{"explicitLocalSelector", "attended", "redactedSummary", "conservativeEffects", "optInQualifiedLocalCoordination"}
		},
		"unknown": func(m map[string]any) { m["future"] = true },
		"duplicate-operation": func(m map[string]any) {
			m["operations"] = []string{"validate", "validate", "diff", "up", "down", "stop", "start", "restart", "rm", "teardown"}
		},
		"metadata-not-operation": func(m map[string]any) { m["version"] = "dill.protocol/1" },
	} {
		t.Run(name, func(t *testing.T) {
			m := operationContractDiscovery(false)
			mutate(m)
			if validOperationDiscovery(operationContractJSON(t, m), false) {
				t.Fatal("unsafe capabilities accepted")
			}
		})
	}
	for _, bad := range [][]byte{ordinary[:len(ordinary)-1], append(append([]byte{}, ordinary...), ordinary...), bytes.Replace(ordinary, []byte(`"version":`), []byte(`"version":"dill.operation/1","version":`), 1)} {
		if validOperationDiscovery(bad, false) {
			t.Fatal("malformed discovery accepted")
		}
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	q := &QualifiedOperationConfig{StateDir: state, Layout: "native-local-docker-v1", DaemonID: "independently-provisioned-test-id"}
	for _, host := range []string{"darwin", "windows", "freebsd"} {
		if validateQualifiedOperationConfig(host, "docker", "/synthetic.sock", q) == nil {
			t.Fatalf("qualified mode accepted unsupported %s", host)
		}
	}
}

func TestOperationContractPinnedFixtureIntegrity(t *testing.T) {
	root := filepath.Join("testdata", "operation-v1")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(operationFixtureHashes) {
		t.Fatal("unexpected fixture inventory")
	}
	for _, entry := range entries {
		expected, ok := operationFixtureHashes[entry.Name()]
		if !ok || entry.Type() != 0 {
			t.Fatalf("unexpected/non-regular fixture %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			t.Fatalf("not exact b3 source bytes: %s", entry.Name())
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version   string   `json:"version"`
		Contract  string   `json:"contract"`
		Fixtures  []string `json:"fixtures"`
		Qualified []string `json:"qualifiedFixtures"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Version != "dill.operation-fixtures/1" || manifest.Contract != "dill.operation/1" {
		t.Fatal("fixture version mismatch")
	}
	if !reflect.DeepEqual(manifest.Fixtures, []string{"success.json", "failure.json", "unsupported-version.json", "unsupported-operation.json"}) || !reflect.DeepEqual(manifest.Qualified, []string{"qualified-success.json", "qualified-cleanup-failure.json"}) {
		t.Fatal("fixture inventory mismatch")
	}
}
