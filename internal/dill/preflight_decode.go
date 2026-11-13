package dill

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"unicode/utf8"
)

var preflightNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
var preflightIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var preflightMessages = map[string]string{
	"INVALID_ARGUMENTS":           "Invalid preflight arguments.",
	"UNSUPPORTED_OPERATION":       "Unsupported preflight operation.",
	"UNSUPPORTED_CONTRACT":        "Unsupported preflight contract.",
	"UNSUPPORTED_FORMAT":          "Unsupported preflight format.",
	"INVALID_TARGET":              "Explicit local Docker target and stack are required.",
	"UNSUPPORTED_PREFLIGHT":       "Unsupported qualified preflight invocation.",
	"SECRET_UNAVAILABLE":          "Secret configuration or source unavailable.",
	"EVALUATION_FAILED":           "Configuration evaluation failed.",
	"CONFIG_INVALID":              "Configuration is invalid.",
	"QUALIFICATION_FAILED":        "Qualified target unavailable.",
	"COORDINATION_UNAVAILABLE":    "Trusted coordination state unavailable. Inspect before retrying.",
	"PREFLIGHT_UNAVAILABLE":       "Complete preflight evidence unavailable.",
	"PREFLIGHT_LIMIT_EXCEEDED":    "Preflight response exceeds supported limits.",
	"CANCELLED":                   "Preflight cancelled or deadline exceeded.",
	"ENGINE_CLOSE_FAILED":         "Preflight transport cleanup failed.",
	"COORDINATION_COMMIT_FAILED":  "Durable preflight outcome unavailable. Inspect before retrying.",
	"COORDINATION_RELEASE_FAILED": "Preflight coordination cleanup failed.",
}

// A frame has no leading/trailing whitespace except one optional terminal LF.
// Interior JSON whitespace is permitted; no second value, CR suffix or prefix.
func preflightFrame(b []byte) bool {
	if len(b) == 0 || len(b) > PreflightMaxOutputBytes || !utf8.Valid(b) {
		return false
	}
	if b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return len(b) > 1 && b[0] == '{' && b[len(b)-1] == '}'
}
func preflightObject(b []byte, keys []string, nullable ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, ErrPreflightContract
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		t, e = d.Token()
		k, ok := t.(string)
		if e != nil || !ok || m[k] != nil || !oneOf(k, keys...) {
			return nil, ErrPreflightContract
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || (bytes.Equal(v, []byte("null")) && !oneOf(k, nullable...)) {
			return nil, ErrPreflightContract
		}
		m[k] = v
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') {
		return nil, ErrPreflightContract
	}
	if _, e = d.Token(); e != io.EOF || len(m) != len(keys) {
		return nil, ErrPreflightContract
	}
	return m, nil
}
func preflightArray(b []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	if len(b) == 0 || b[0] != '[' || json.Unmarshal(b, &a) != nil || a == nil {
		return nil, ErrPreflightContract
	}
	return a, nil
}

func DecodePreflightCapabilities(b []byte) (*PreflightCapabilities, error) {
	if !preflightFrame(b) {
		return nil, ErrPreflightContract
	}
	var c PreflightCapabilities
	if json.Unmarshal(b, &c) != nil || c.Version != PreflightCapabilitiesVersion {
		return nil, ErrPreflightContract
	}
	keys := []string{"version", "supported"}
	if c.Supported {
		keys = append(keys, "contract", "applyContract", "operations", "engines", "layouts", "limits")
	}
	m, e := preflightObject(b, keys)
	if e != nil {
		return nil, ErrPreflightContract
	}
	if c.Supported {
		if c.Contract != PreflightVersion || c.ApplyContract != OperationVersion || !exactOperationSet(m["operations"], []string{"up", "start", "restart", "stop", "down"}) || !exactOperationSet(m["engines"], []string{"docker"}) || !exactOperationSet(m["layouts"], []string{"native-local-docker-v1"}) {
			return nil, ErrPreflightContract
		}
		if _, e = preflightObject(m["limits"], []string{"maxResources", "maxNameBytes", "maxOutputBytes"}); e != nil || c.Limits == nil || *c.Limits != (PreflightLimits{256, 255, 262144}) {
			return nil, ErrPreflightContract
		}
	}
	return &c, nil
}

// DecodePreflightResult validates wire evidence only, without process or provenance.
func DecodePreflightResult(b []byte) (*PreflightResult, error) {
	if !preflightFrame(b) {
		return nil, ErrPreflightContract
	}
	m, e := preflightObject(b, []string{"version", "operationId", "operation", "target", "stack", "outcome", "engineEffects", "coordinationEffects", "inputDigest", "selectionDigest", "coverage", "resources", "resourcesTruncated", "errors"}, "target", "inputDigest", "selectionDigest")
	if e != nil {
		return nil, ErrPreflightContract
	}
	var r PreflightResult
	if json.Unmarshal(b, &r) != nil {
		return nil, ErrPreflightContract
	}
	if r.Target != nil {
		if _, e = preflightObject(m["target"], []string{"engine", "endpointDigest", "qualifiedIdentityDigest"}); e != nil {
			return nil, ErrPreflightContract
		}
	}
	cm, e := preflightObject(m["coverage"], []string{"containers", "network", "volumes"})
	if e != nil {
		return nil, ErrPreflightContract
	}
	for _, raw := range cm {
		if _, e = preflightObject(raw, []string{"scope", "count", "presentCount", "complete"}); e != nil {
			return nil, ErrPreflightContract
		}
	}
	rows, e := preflightArray(m["resources"])
	if e != nil || len(rows) > 256 {
		return nil, ErrPreflightContract
	}
	for _, raw := range rows {
		rm, err := preflightObject(raw, []string{"kind", "name", "identity", "identityKind", "presence", "action", "retention"})
		if err != nil {
			return nil, ErrPreflightContract
		}
		// Display names and identities are literal ASCII, never escaping-expanded.
		for _, k := range []string{"name", "identity"} {
			if bytes.Contains(rm[k], []byte{'\\'}) {
				return nil, ErrPreflightContract
			}
		}
	}
	es, e := preflightArray(m["errors"])
	if e != nil || len(es) > 4 {
		return nil, ErrPreflightContract
	}
	for _, raw := range es {
		if _, e = preflightObject(raw, []string{"code", "message"}); e != nil {
			return nil, ErrPreflightContract
		}
	}
	if !validPreflightResult(&r) {
		return nil, ErrPreflightContract
	}
	return &r, nil
}

func validPreflightResult(r *PreflightResult) bool {
	if r == nil || r.Version != PreflightVersion || !operationIDPattern.MatchString(r.OperationID) || (!preflightAction(r.Operation) && r.Operation != "") ||
		(r.Stack != "" && !operationStackPattern.MatchString(r.Stack)) || r.EngineEffects != "none" || !oneOf(r.CoordinationEffects, "none", "written", "unknown") ||
		!oneOf(r.Outcome, "succeeded", "failed", "cancelled", "unknown", "invalid_input") || r.Resources == nil || r.Errors == nil || len(r.Resources) > 256 || len(r.Errors) > 4 {
		return false
	}
	if r.Target != nil && (r.Target.Engine != "docker" || !operationDigestPattern.MatchString(r.Target.EndpointDigest) || !operationDigestPattern.MatchString(r.Target.QualifiedIdentityDigest)) {
		return false
	}
	success := r.Outcome == "succeeded"
	if success {
		if r.Target == nil || r.Stack == "" || !preflightAction(r.Operation) || r.CoordinationEffects != "written" || len(r.Errors) != 0 || r.ResourcesTruncated || r.InputDigest == nil || r.SelectionDigest == nil || !operationDigestPattern.MatchString(*r.InputDigest) || !operationDigestPattern.MatchString(*r.SelectionDigest) {
			return false
		}
	} else if len(r.Errors) == 0 || len(r.Resources) != 0 || r.InputDigest != nil || r.SelectionDigest != nil {
		return false
	}
	counts, present := map[string]int{}, map[string]int{}
	seen := map[string]bool{}
	presentContainerIDs := map[string]bool{}
	creates := false
	volumeEnsure := false
	for _, x := range r.Resources {
		key := x.Kind + "\x00" + x.Name
		if !validPreflightResource(x, r.Operation) || seen[key] {
			return false
		}
		seen[key] = true
		// Distinct authoritative container names cannot share one live ID.
		// Keep identity namespaces separate: network IDs and absent planned
		// names are not live container identities, and rows are never collapsed.
		if x.Kind == "container" && x.Presence == "present" {
			if presentContainerIDs[x.Identity] {
				return false
			}
			presentContainerIDs[x.Identity] = true
		}
		counts[x.Kind]++
		if x.Presence == "present" {
			present[x.Kind]++
		}
		if x.Kind == "container" && oneOf(x.Action, "create", "recreate") {
			creates = true
		}
		if x.Kind == "volume" && x.Action == "ensure" {
			volumeEnsure = true
		}
	}
	if volumeEnsure && !creates {
		return false
	}
	for _, s := range []struct {
		kind, scope string
		v           PreflightScope
	}{{"container", "declared-stack", r.Coverage.Containers}, {"network", "stack-network", r.Coverage.Network}, {"volume", "declared-attached-named", r.Coverage.Volumes}} {
		if s.v.Scope != s.scope || s.v.Count < 0 || s.v.Count > 256 || s.v.PresentCount < 0 || s.v.PresentCount > s.v.Count || s.v.Count != counts[s.kind] || s.v.PresentCount != present[s.kind] || s.v.Complete != success {
			return false
		}
	}
	if success {
		return counts["network"] == 1
	}
	return validPreflightErrors(r)
}
func validPreflightResource(x PreflightResource, op string) bool {
	if !preflightNamePattern.MatchString(x.Name) || !oneOf(x.Presence, "present", "absent") {
		return false
	}
	live := x.Presence == "present"
	if !live {
		if x.IdentityKind != "planned-name" || x.Identity != x.Name {
			return false
		}
	} else if x.Kind == "volume" {
		if x.IdentityKind != "engine-name" || x.Identity != x.Name {
			return false
		}
	} else if x.IdentityKind != "immutable-id" || !preflightIDPattern.MatchString(x.Identity) {
		return false
	}
	switch x.Kind {
	case "container":
		if x.Retention != "not-applicable" {
			return false
		}
		switch op {
		case "up":
			if live {
				return oneOf(x.Action, "recreate", "ensure-started")
			}
			return x.Action == "create"
		case "start", "restart":
			return live && x.Action == op
		case "stop":
			return (live && x.Action == "stop") || (!live && x.Action == "none")
		case "down":
			return (live && x.Action == "remove") || (!live && x.Action == "none")
		}
	case "network":
		return x.Retention == "preserve" && ((oneOf(op, "up", "start", "restart") && x.Action == "ensure") || (oneOf(op, "stop", "down") && x.Action == "retain"))
	case "volume":
		return x.Retention == "preserve" && (x.Action == "retain" || (op == "up" && x.Action == "ensure"))
	}
	return false
}
func validPreflightErrors(r *PreflightResult) bool {
	last := 0
	primary := ""
	limit := false
	cleanup := false
	for i, x := range r.Errors {
		msg, ok := preflightMessages[x.Code]
		if !ok || x.Message != msg {
			return false
		}
		rank := 0
		switch x.Code {
		case "ENGINE_CLOSE_FAILED":
			rank = 1
		case "COORDINATION_COMMIT_FAILED":
			rank = 2
		case "COORDINATION_RELEASE_FAILED":
			rank = 3
		}
		if rank == 0 {
			if i != 0 {
				return false
			}
			primary = x.Code
		} else {
			if rank <= last {
				return false
			}
			last = rank
			cleanup = true
		}
		if x.Code == "PREFLIGHT_LIMIT_EXCEEDED" {
			limit = true
		}
	}
	if r.ResourcesTruncated != limit {
		return false
	}
	if primary == "SECRET_UNAVAILABLE" && (r.Target != nil || r.Stack != "") {
		return false
	}
	if cleanup {
		return r.Outcome == "unknown" && oneOf(r.CoordinationEffects, "none", "unknown")
	}
	switch primary {
	case "INVALID_ARGUMENTS", "UNSUPPORTED_OPERATION", "UNSUPPORTED_CONTRACT", "UNSUPPORTED_FORMAT", "INVALID_TARGET", "UNSUPPORTED_PREFLIGHT":
		return r.Outcome == "invalid_input" && r.CoordinationEffects == "none"
	case "CONFIG_INVALID":
		return (r.Outcome == "invalid_input" && r.CoordinationEffects == "none") || (r.Outcome == "failed" && r.CoordinationEffects == "written")
	case "CANCELLED":
		return r.Outcome == "cancelled" && r.CoordinationEffects != "unknown"
	case "COORDINATION_UNAVAILABLE":
		return oneOf(r.Outcome, "failed", "unknown") && r.CoordinationEffects == "unknown"
	case "SECRET_UNAVAILABLE", "EVALUATION_FAILED", "QUALIFICATION_FAILED":
		return r.Outcome == "failed" && r.CoordinationEffects == "none"
	case "PREFLIGHT_UNAVAILABLE", "PREFLIGHT_LIMIT_EXCEEDED":
		return r.Outcome == "failed" && r.CoordinationEffects != "unknown"
	}
	return false
}

// ObservePreflightInvocation never promotes malformed delivery to a result.
// processExit must come independently from the subprocess supervisor, not stdout.
func ObservePreflightInvocation(b []byte, processExit *int) PreflightInvocation {
	v := PreflightInvocation{Status: "unknown"}
	if processExit == nil {
		return v
	}
	n := *processExit
	v.ProcessExit = &n
	if n < 0 || n > 255 {
		return v
	}
	r, e := DecodePreflightResult(b)
	if e != nil {
		return v
	}
	want := 1
	if r.Outcome == "succeeded" {
		want = 0
	} else if r.Outcome == "invalid_input" {
		want = 2
	}
	if n != want {
		return v
	}
	v.Result = r
	switch r.Outcome {
	case "succeeded":
		v.Status = "succeeded"
	case "cancelled":
		v.Status = "cancelled"
	case "failed", "invalid_input":
		v.Status = "failed"
	}
	return v
}

// DecodePreflightInvocation is a pure local DTO decoder, not a provenance gate.
func DecodePreflightInvocation(b []byte) (*PreflightInvocation, error) {
	if !preflightFrame(b) {
		return nil, ErrPreflightContract
	}
	m, e := preflightObject(b, []string{"result", "processExit", "status"}, "result", "processExit")
	if e != nil {
		return nil, ErrPreflightContract
	}
	var v PreflightInvocation
	if json.Unmarshal(b, &v) != nil || (v.ProcessExit != nil && (*v.ProcessExit < -1 || *v.ProcessExit > 255)) {
		return nil, ErrPreflightContract
	}
	if v.Result == nil {
		if v.Status != "unknown" {
			return nil, ErrPreflightContract
		}
		return &v, nil
	}
	observed := ObservePreflightInvocation(m["result"], v.ProcessExit)
	if observed.Result == nil || observed.Status != v.Status {
		return nil, ErrPreflightContract
	}
	return &observed, nil
}
