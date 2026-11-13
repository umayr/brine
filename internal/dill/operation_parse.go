package dill

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxOperationOutput = 2 * 1024 * 1024

var operationIDPattern = regexp.MustCompile(`^op_[0-9a-f]{32}$`)
var operationDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var operationStackPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var operationNames = []string{"validate", "plan", "diff", "up", "down", "stop", "start", "restart", "rm", "teardown"}
var qualifiedOperationNames = []string{"up", "down", "stop", "start", "restart", "rm"}
var operationFeatures = []string{"explicitLocalSelector", "attended", "redactedSummary", "conservativeEffects"}
var operationMessages = map[string]string{
	"ENGINE_CLOSE_FAILED":              "Engine connection release failed. Inspect the target before retrying.",
	"COORDINATION_COMMIT_FAILED":       "Durable outcome unavailable. Inspect coordination state before retrying.",
	"COORDINATION_RELEASE_FAILED":      "Engine outcome was recorded; local coordination cleanup failed.",
	"SECRET_UNAVAILABLE":               "Secret configuration or source unavailable.",
	"UNSUPPORTED_OPERATION":            "Unsupported operation. Use operation-capabilities discovery.",
	"INVALID_ARGUMENTS":                "Invalid operation arguments.",
	"UNSUPPORTED_CONTRACT":             "Unsupported operation contract. Use operation-capabilities discovery.",
	"UNSUPPORTED_FORMAT":               "Unsupported operation format.",
	"INVALID_TARGET":                   "Explicit local engine, socket and valid stack are required.",
	"UNSUPPORTED_COORDINATION":         "Unsupported qualified coordination invocation.",
	"CANCELLED":                        "Operation cancelled or deadline exceeded.",
	"COORDINATION_INSPECTION_REQUIRED": "Unresolved coordination state. Inspect and explicitly acknowledge it before retrying.",
	"INVALID_INPUT":                    "Invalid operation input.",
	"OPERATION_FAILED":                 "Operation failed. Inspect the target before retrying if effects are uncertain.",
}

func oneOf(v string, values ...string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}

// operationObject requires exactly the known fields and rejects duplicates at
// each object level. Callers recursively validate each nested object/array.
func operationObject(data []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if len(data) > maxOperationOutput || !utf8.Valid(data) {
		return nil, ErrOperationContract
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, ErrOperationContract
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		t, e = d.Token()
		k, ok := t.(string)
		if e != nil || !ok || m[k] != nil || (!oneOf(k, required...) && !oneOf(k, optional...)) {
			return nil, ErrOperationContract
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrOperationContract
		}
		m[k] = v
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') {
		return nil, ErrOperationContract
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrOperationContract
	}
	for _, k := range required {
		if m[k] == nil {
			return nil, ErrOperationContract
		}
	}
	for k, v := range m {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && k != "target" && k != "receipt" && k != "exitCode" {
			return nil, ErrOperationContract
		}
	}
	return m, nil
}
func operationArray(data []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	if len(data) == 0 || data[0] != '[' || json.Unmarshal(data, &a) != nil || a == nil {
		return nil, ErrOperationContract
	}
	return a, nil
}
func exactOperationSet(data []byte, want []string) bool {
	a, e := operationArray(data)
	if e != nil || len(a) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, raw := range a {
		var s string
		if json.Unmarshal(raw, &s) != nil || !oneOf(s, want...) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func validOperationDiscovery(data []byte, qualified bool) bool {
	if !operationLine(data, 8192) {
		return false
	}
	m, e := operationObject(data, []string{"version", "operations", "engines", "features"}, []string{"qualifiedCoordinationOperations", "qualifiedCoordinationEngines", "qualifiedCoordinationLayouts"})
	if e != nil {
		return false
	}
	var version string
	if json.Unmarshal(m["version"], &version) != nil || version != OperationVersion || !exactOperationSet(m["operations"], operationNames) || !exactOperationSet(m["engines"], []string{"docker", "podman"}) {
		return false
	}
	has := len(m) == 7
	if len(m) != 4 && !has {
		return false
	}
	features := append([]string{}, operationFeatures...)
	if has {
		features = append(features, "optInQualifiedLocalCoordination")
	}
	if !exactOperationSet(m["features"], features) {
		return false
	}
	if has && (!exactOperationSet(m["qualifiedCoordinationOperations"], qualifiedOperationNames) || !exactOperationSet(m["qualifiedCoordinationEngines"], []string{"docker"}) || !exactOperationSet(m["qualifiedCoordinationLayouts"], []string{"native-local-docker-v1"})) {
		return false
	}
	return !qualified || has
}
func operationLine(data []byte, max int) bool {
	return len(data) > 1 && len(data) <= max && data[len(data)-1] == '\n' && bytes.Count(data, []byte{'\n'}) == 1 && bytes.Count(data, []byte{'\r'}) == 0 && utf8.Valid(data)
}

func parseOperationReceipt(data []byte) (*OperationReceipt, error) {
	m, e := operationObject(data, []string{"version", "operationId", "operation", "target", "stack", "outcome", "effects", "changesDetected", "resources", "resourcesTruncated", "errors"}, nil)
	if e != nil {
		return nil, e
	}
	var r OperationReceipt
	if json.Unmarshal(data, &r) != nil || r.Version != OperationVersion || !operationIDPattern.MatchString(r.OperationID) || (!oneOf(r.Operation, operationNames...) && r.Operation != "") || !oneOf(r.Outcome, "succeeded", "changes", "invalid_input", "failed", "cancelled", "unknown") || !oneOf(r.Effects, "none", "complete", "partial", "unknown") {
		return nil, ErrOperationContract
	}
	if r.Target != nil {
		tm, e := operationObject(m["target"], []string{"engine", "endpointDigest"}, []string{"qualifiedIdentityDigest"})
		if e != nil || !oneOf(r.Target.Engine, "docker", "podman") || !operationDigestPattern.MatchString(r.Target.EndpointDigest) || !operationStackPattern.MatchString(r.Stack) {
			return nil, ErrOperationContract
		}
		if tm["qualifiedIdentityDigest"] != nil && !operationDigestPattern.MatchString(r.Target.QualifiedIdentityDigest) {
			return nil, ErrOperationContract
		}
	} else if r.Stack != "" || r.Effects != "none" || r.ChangesDetected || len(r.Resources) != 0 || !oneOf(r.Outcome, "invalid_input", "failed") {
		return nil, ErrOperationContract
	}
	resources, e := operationArray(m["resources"])
	if e != nil || len(resources) > 4096 {
		return nil, ErrOperationContract
	}
	for i, raw := range resources {
		if _, e = operationObject(raw, []string{"kind", "identityDigest", "action", "effect"}, nil); e != nil {
			return nil, e
		}
		x := r.Resources[i]
		if !operationDigestPattern.MatchString(x.IdentityDigest) || !oneOf(x.Effect, "none", "complete", "unknown") {
			return nil, ErrOperationContract
		}
		valid := false
		switch x.Kind {
		case "service":
			valid = oneOf(x.Action, "create", "recreate", "remove", "noop") && x.Effect == "none"
		case "container":
			valid = oneOf(x.Action, "stop", "start", "remove", "intent.stopRemove", "intent.removeStoppedCandidate")
		case "createScope":
			valid = x.Action == "create"
		case "network", "volume":
			valid = oneOf(x.Action, "ensure", "intent.retain")
		case "image":
			valid = oneOf(x.Action, "pull", "remove", "intent.removeCandidate")
		}
		if !valid || (strings.HasPrefix(x.Action, "intent.") && x.Effect != "none") {
			return nil, ErrOperationContract
		}
	}
	diagnostics, e := operationArray(m["errors"])
	if e != nil || len(diagnostics) > 16 {
		return nil, ErrOperationContract
	}
	for i, raw := range diagnostics {
		if _, e = operationObject(raw, []string{"code", "message"}, nil); e != nil {
			return nil, e
		}
		x := r.Errors[i]
		msg, ok := operationMessages[x.Code]
		if !ok || len(x.Message) > 256 {
			return nil, ErrOperationContract
		}
		if x.Message != msg && !(x.Code == "INVALID_ARGUMENTS" && oneOf(x.Message, "Unsupported options for this operation.", "Invalid pull policy.", "Unexpected operation arguments.")) {
			return nil, ErrOperationContract
		}
	}
	if r.Operation == "" && !(r.Outcome == "invalid_input" && len(r.Errors) > 0 && r.Errors[0].Code == "UNSUPPORTED_OPERATION") {
		return nil, ErrOperationContract
	}
	if oneOf(r.Operation, "plan", "diff", "validate") && r.Effects != "none" {
		return nil, ErrOperationContract
	}
	if r.Outcome == "changes" && (!oneOf(r.Operation, "plan", "diff") || !r.ChangesDetected) {
		return nil, ErrOperationContract
	}
	if r.Outcome == "unknown" && r.Effects != "unknown" {
		return nil, ErrOperationContract
	}
	return &r, nil
}
func invocationStatus(r *OperationReceipt, exit int) string {
	if exit < 0 || exit > 255 {
		return "unknown"
	}
	switch r.Outcome {
	case "succeeded":
		if exit == 0 && len(r.Errors) == 0 {
			return "succeeded"
		}
		if exit == 1 && len(r.Errors) > 0 {
			return "failed"
		}
	case "changes":
		want := 0
		if r.Operation == "plan" {
			want = 1
		}
		if exit == want && len(r.Errors) == 0 {
			return "changes"
		}
		if exit == 1 && len(r.Errors) > 0 {
			return "failed"
		}
	case "cancelled":
		if exit == 0 || exit == 1 {
			return "cancelled"
		}
	case "invalid_input":
		if exit == 2 {
			return "failed"
		}
	case "failed":
		if exit == 1 {
			return "failed"
		}
	case "unknown":
		if exit == 1 {
			return "unknown"
		}
	}
	return "unknown"
}
