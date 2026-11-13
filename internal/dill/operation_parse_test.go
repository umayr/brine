package dill

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func consumerReceipt(op string) *OperationReceipt {
	return &OperationReceipt{Version: OperationVersion, OperationID: "op_0123456789abcdef0123456789abcdef", Operation: op, Target: &OperationTarget{Engine: "docker", EndpointDigest: operationDigest("unix:///synthetic/engine.sock")}, Stack: "app", Outcome: "succeeded", Effects: "none", Resources: []OperationResource{}, Errors: []OperationDiagnostic{}}
}
func consumerJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func consumerDiscovery(qualified bool) []byte {
	m := map[string]any{"version": OperationVersion, "operations": operationNames, "engines": []string{"docker", "podman"}, "features": operationFeatures}
	if qualified {
		m["features"] = append(append([]string{}, operationFeatures...), "optInQualifiedLocalCoordination")
		m["qualifiedCoordinationOperations"] = qualifiedOperationNames
		m["qualifiedCoordinationEngines"] = []string{"docker"}
		m["qualifiedCoordinationLayouts"] = []string{"native-local-docker-v1"}
	}
	b, _ := json.Marshal(m)
	return append(b, '\n')
}
func TestOperationStrictReceiptAdversaries(t *testing.T) {
	good := string(consumerJSON(t, consumerReceipt("plan")))
	cases := map[string]string{
		"unknown":          strings.Replace(good, `"version":`, `"extra":1,"version":`, 1),
		"duplicate":        strings.Replace(good, `"version":`, `"version":"dill.operation/1","version":`, 1),
		"missing":          strings.Replace(good, `"changesDetected":false,`, "", 1),
		"null-bool":        strings.Replace(good, `"changesDetected":false`, `"changesDetected":null`, 1),
		"null-array":       strings.Replace(good, `"resources":[]`, `"resources":null`, 1),
		"null-digest":      strings.Replace(good, `"endpointDigest":"`+operationDigest("unix:///synthetic/engine.sock")+`"`, `"endpointDigest":null`, 1),
		"nested-unknown":   strings.Replace(good, `"engine":"docker"`, `"engine":"docker","extra":1`, 1),
		"nested-duplicate": strings.Replace(good, `"engine":"docker"`, `"engine":"docker","engine":"docker"`, 1),
		"null-qualified":   strings.Replace(good, `"engine":"docker"`, `"engine":"docker","qualifiedIdentityDigest":null`, 1),
		"empty-qualified":  strings.Replace(good, `"engine":"docker"`, `"engine":"docker","qualifiedIdentityDigest":""`, 1),
		"bad-id":           strings.Replace(good, "op_0123456789abcdef0123456789abcdef", "op_ABCDEF", 1),
		"bad-outcome":      strings.Replace(good, `"outcome":"succeeded"`, `"outcome":"ok"`, 1),
		"bad-effects":      strings.Replace(good, `"effects":"none"`, `"effects":"transactional"`, 1),
		"success-unknown":  strings.Replace(good, `"effects":"none"`, `"effects":"unknown"`, 1),
		"success-partial":  strings.Replace(good, `"effects":"none"`, `"effects":"partial"`, 1),
		"trailing":         good + "LEAK",
		"multi":            good + good,
		"utf8":             good[:10] + string([]byte{0xff}) + good[10:],
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if r, e := parseOperationReceipt([]byte(data)); e == nil || r != nil {
				t.Fatalf("accepted malformed receipt: %v", e)
			}
		})
	}
	if _, e := parseOperationReceipt([]byte(good)); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{good + "\n\n", good + " \n", " " + good + "\n", good + "\r\n", good, good + "\n ", good + "\n" + good + "\n"} {
		if operationLine([]byte(raw), maxOperationOutput) {
			t.Fatal("accepted malformed framing")
		}
	}
	if !operationLine([]byte(good+"\n"), maxOperationOutput) {
		t.Fatal("valid framing rejected")
	}
}
func TestOperationStrictDiscoveryPresenceRules(t *testing.T) {
	for _, qualified := range []bool{false, true} {
		good := consumerDiscovery(qualified)
		if !validOperationDiscovery(good, qualified) {
			t.Fatal("valid discovery rejected")
		}
		if !qualified && validOperationDiscovery(good, true) {
			t.Fatal("ordinary discovery qualified")
		}
		var m map[string]any
		_ = json.Unmarshal(good, &m)
		for _, field := range []string{"version", "operations", "engines", "features"} {
			copy := map[string]any{}
			for k, v := range m {
				copy[k] = v
			}
			delete(copy, field)
			if validOperationDiscovery(append(consumerJSON(t, copy), '\n'), false) {
				t.Fatalf("missing %s accepted", field)
			}
		}
	}
	for _, field := range []string{"qualifiedCoordinationOperations", "qualifiedCoordinationEngines", "qualifiedCoordinationLayouts"} {
		var m map[string]any
		_ = json.Unmarshal(consumerDiscovery(true), &m)
		for _, bad := range []any{nil, []string{}, []string{"unknown"}, []string{"docker", "docker"}} {
			m[field] = bad
			if validOperationDiscovery(append(consumerJSON(t, m), '\n'), true) {
				t.Fatalf("invalid %s accepted", field)
			}
		}
		delete(m, field)
		if validOperationDiscovery(append(consumerJSON(t, m), '\n'), false) {
			t.Fatal("partial qualified presence accepted")
		}
	}
	for _, good := range [][]byte{consumerDiscovery(false), consumerDiscovery(true)} {
		for _, bad := range [][]byte{bytes.Replace(good, []byte(`"version":`), []byte(`"unknown":true,"version":`), 1), bytes.Replace(good, []byte(`"version":`), []byte(`"version":"dill.operation/1","version":`), 1), append(good, '\n')} {
			if validOperationDiscovery(bad, false) {
				t.Fatal("malformed discovery accepted")
			}
		}
	}
}
func TestOperationBudgetsAndClosedResources(t *testing.T) {
	r := consumerReceipt("up")
	r.Effects = "complete"
	r.Resources = []OperationResource{{Kind: "container", IdentityDigest: operationDigest("x"), Action: "stop", Effect: "complete"}}
	if _, e := parseOperationReceipt(consumerJSON(t, r)); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(){func() { r.Resources[0].Kind = "raw-container-name" }, func() { r.Resources[0].Action = "raw-command" }, func() { r.Resources[0].Effect = "partial" }, func() { r.Resources[0].IdentityDigest = "sha256:UPPER" }} {
		saved := r.Resources[0]
		mutate()
		if _, e := parseOperationReceipt(consumerJSON(t, r)); e == nil {
			t.Fatal("malformed resource accepted")
		}
		r.Resources[0] = saved
	}
	for _, field := range []string{"kind", "identityDigest", "action", "effect"} {
		raw := string(consumerJSON(t, r))
		var m map[string]any
		_ = json.Unmarshal(consumerJSON(t, r.Resources[0]), &m)
		delete(m, field)
		raw = strings.Replace(raw, string(consumerJSON(t, r.Resources[0])), string(consumerJSON(t, m)), 1)
		if _, e := parseOperationReceipt([]byte(raw)); e == nil {
			t.Fatal("missing resource field accepted")
		}
	}
	r.Resources = make([]OperationResource, 4097)
	if _, e := parseOperationReceipt(consumerJSON(t, r)); e == nil {
		t.Fatal("resource overflow accepted")
	}
	r.Resources = []OperationResource{}
	r.Errors = []OperationDiagnostic{{Code: "OPERATION_FAILED", Message: "SECRET RAW DIAGNOSTIC"}}
	if _, e := parseOperationReceipt(consumerJSON(t, r)); e == nil {
		t.Fatal("raw error accepted")
	}
	r.Errors = make([]OperationDiagnostic, 17)
	if _, e := parseOperationReceipt(consumerJSON(t, r)); e == nil {
		t.Fatal("diagnostic overflow accepted")
	}
	if _, e := parseOperationReceipt(bytes.Repeat([]byte{' '}, maxOperationOutput+1)); e == nil {
		t.Fatal("byte overflow accepted")
	}
}
func TestOperationRequestBindingAndExitAxes(t *testing.T) {
	r := consumerReceipt("stop")
	q := &QualifiedOperationConfig{DaemonID: "synthetic-docker-host-1234"}
	r.Target.QualifiedIdentityDigest = operationDigest("dill-local-docker-v1\x00" + q.DaemonID)
	r.Effects = "complete"
	if e := bindOperationReceipt(r, "stop", "app", "docker", "unix:///synthetic/engine.sock", q); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][4]string{{"start", "app", "docker", "unix:///synthetic/engine.sock"}, {"stop", "other", "docker", "unix:///synthetic/engine.sock"}, {"stop", "app", "podman", "unix:///synthetic/engine.sock"}, {"stop", "app", "docker", "unix:///other.sock"}} {
		if bindOperationReceipt(r, args[0], args[1], args[2], args[3], q) == nil {
			t.Fatal("request mismatch accepted")
		}
	}
	r.Target.QualifiedIdentityDigest = ""
	if bindOperationReceipt(r, "stop", "app", "docker", "unix:///synthetic/engine.sock", q) == nil {
		t.Fatal("unqualified success accepted")
	}
	r.Target.QualifiedIdentityDigest = operationDigest("wrong")
	if bindOperationReceipt(r, "stop", "app", "docker", "unix:///synthetic/engine.sock", q) == nil {
		t.Fatal("wrong daemon accepted")
	}
	r.Errors = []OperationDiagnostic{{Code: "COORDINATION_RELEASE_FAILED", Message: operationMessages["COORDINATION_RELEASE_FAILED"]}}
	if invocationStatus(r, 1) != "failed" || !validOperationExit(r, 1) {
		t.Fatal("release failure became success")
	}
	r.Outcome = "unknown"
	r.Effects = "unknown"
	if validOperationExit(r, 2) {
		t.Fatal("unknown exit2 accepted")
	}
	r = consumerReceipt("plan")
	r.Outcome = "changes"
	r.ChangesDetected = true
	if invocationStatus(r, 1) != "changes" || validOperationExit(r, 0) {
		t.Fatal("plan exit mismatch")
	}
	r = consumerReceipt("stop")
	r.Outcome = "cancelled"
	if invocationStatus(r, 0) != "cancelled" {
		t.Fatal("declined cancellation lost")
	}
	r.Errors = []OperationDiagnostic{{Code: "CANCELLED", Message: operationMessages["CANCELLED"]}}
	if invocationStatus(r, 1) != "cancelled" {
		t.Fatal("deadline cancellation lost")
	}
}
func TestOperationInvocationDecoder(t *testing.T) {
	for _, v := range []OperationInvocation{{Receipt: nil, Status: "unknown", ExitCode: -1}, {Receipt: consumerReceipt("validate"), Status: "succeeded", ExitCode: 0}} {
		if _, e := DecodeOperationInvocation(consumerJSON(t, v)); e != nil {
			t.Fatal(e)
		}
	}
	for _, data := range []string{`{"receipt":null,"status":"succeeded","exitCode":0}`, `{"receipt":null,"status":"unknown","exitCode":null}`, `{"receipt":null,"status":"unknown","exitCode":-2}`, `{"receipt":null,"status":"unknown","exitCode":0,"raw":"LEAK"}`} {
		if _, e := DecodeOperationInvocation([]byte(data)); e == nil {
			t.Fatal("invalid invocation accepted")
		}
	}
}
