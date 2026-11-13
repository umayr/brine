package dill

import (
	"encoding/json"
	"strings"
	"testing"
)

// Independently literal action-axis expectations extend (rather than infer from)
// the phase cross-product, whose original operation axis was fixed to up.
func TestPreflightKnownAndEmptyActionAxis(t *testing.T) {
	type scenario struct {
		name, code, message, outcome, coordination, stack string
		cleanup                                           []string
		target, allowEmpty                                bool
	}
	scenarios := []scenario{}
	for _, entry := range preflightFrozenDiagnostics {
		empty := false
		switch entry.code {
		case "INVALID_ARGUMENTS", "UNSUPPORTED_OPERATION", "UNSUPPORTED_CONTRACT", "UNSUPPORTED_FORMAT", "INVALID_TARGET", "UNSUPPORTED_PREFLIGHT", "SECRET_UNAVAILABLE":
			empty = true
		}
		scenarios = append(scenarios, scenario{name: entry.code, code: entry.code, message: entry.message, outcome: entry.outcome, coordination: entry.coordination, allowEmpty: empty})
	}
	scenarios = append(scenarios,
		scenario{name: "late-secret-written", code: "SECRET_UNAVAILABLE", message: "Secret configuration or source unavailable.", outcome: "failed", coordination: "written"},
		scenario{name: "secret-close-prestate", code: "SECRET_UNAVAILABLE", message: "Secret configuration or source unavailable.", outcome: "unknown", coordination: "none", cleanup: []string{"ENGINE_CLOSE_FAILED"}},
		scenario{name: "secret-close-state", code: "SECRET_UNAVAILABLE", message: "Secret configuration or source unavailable.", outcome: "unknown", coordination: "unknown", cleanup: []string{"ENGINE_CLOSE_FAILED"}},
		scenario{name: "secret-commit", code: "SECRET_UNAVAILABLE", message: "Secret configuration or source unavailable.", outcome: "unknown", coordination: "unknown", cleanup: []string{"COORDINATION_COMMIT_FAILED"}},
		scenario{name: "secret-release", code: "SECRET_UNAVAILABLE", message: "Secret configuration or source unavailable.", outcome: "unknown", coordination: "unknown", cleanup: []string{"COORDINATION_RELEASE_FAILED"}},
		scenario{name: "evaluation-close", code: "EVALUATION_FAILED", message: "Configuration evaluation failed.", outcome: "unknown", coordination: "none", cleanup: []string{"ENGINE_CLOSE_FAILED"}},
		scenario{name: "qualification-close", code: "QUALIFICATION_FAILED", message: "Qualified target unavailable.", outcome: "unknown", coordination: "none", cleanup: []string{"ENGINE_CLOSE_FAILED"}},
		scenario{name: "actual-config-before-begin", code: "CONFIG_INVALID", message: "Configuration is invalid.", outcome: "invalid_input", coordination: "none", target: true, stack: "synthetic"},
		scenario{name: "actual-config-late", code: "CONFIG_INVALID", message: "Configuration is invalid.", outcome: "failed", coordination: "written", target: true, stack: "synthetic"},
		scenario{name: "actual-cancel-before-open", code: "CANCELLED", message: "Preflight cancelled or deadline exceeded.", outcome: "cancelled", coordination: "none", target: true, stack: "synthetic"},
		scenario{name: "actual-cancel-after-begin", code: "CANCELLED", message: "Preflight cancelled or deadline exceeded.", outcome: "cancelled", coordination: "written", target: true, stack: "synthetic"},
	)
	cleanupMessages := map[string]string{
		"ENGINE_CLOSE_FAILED":         "Preflight transport cleanup failed.",
		"COORDINATION_COMMIT_FAILED":  "Durable preflight outcome unavailable. Inspect before retrying.",
		"COORDINATION_RELEASE_FAILED": "Preflight coordination cleanup failed. Inspect before retrying.",
	}
	for _, s := range scenarios {
		for _, action := range []string{"up", "start", "restart", "stop", "down", ""} {
			label := action
			if label == "" {
				label = "EMPTY"
			}
			t.Run(s.name+"/"+label, func(t *testing.T) {
				f := preflightFixtureRead(t, "preview-limit-failure.json")
				var m map[string]any
				if json.Unmarshal(f.Stdout, &m) != nil {
					t.Fatal("fixture")
				}
				m["operation"] = action
				m["target"] = nil
				m["stack"] = s.stack
				m["outcome"] = s.outcome
				m["coordinationEffects"] = s.coordination
				m["resourcesTruncated"] = s.code == "PREFLIGHT_LIMIT_EXCEEDED"
				if s.target {
					m["target"] = map[string]any{"engine": "docker", "endpointDigest": "sha256:" + strings.Repeat("1", 64), "qualifiedIdentityDigest": "sha256:" + strings.Repeat("2", 64)}
				}
				errors := []any{map[string]any{"code": s.code, "message": s.message}}
				for _, code := range s.cleanup {
					errors = append(errors, map[string]any{"code": code, "message": cleanupMessages[code]})
				}
				m["errors"] = errors
				b := preflightJSON(t, m)
				want := action != "" || s.allowEmpty
				r, e := DecodePreflightResult(b)
				if (r != nil && e == nil) != want {
					t.Fatalf("action-axis result gotValid=%t want=%t", r != nil && e == nil, want)
				}
				exit := 1
				if s.outcome == "invalid_input" {
					exit = 2
				}
				v := ObservePreflightInvocation(b, &exit)
				if (v.Result != nil) != want || (!want && v.Status != "unknown") {
					t.Fatal("action-axis observer")
				}
				status := "unknown"
				if want {
					status = v.Status
				}
				local := map[string]any{"result": json.RawMessage(b), "processExit": exit, "status": status}
				decoded, e := DecodePreflightInvocation(preflightJSON(t, local))
				if (decoded != nil && decoded.Result != nil && e == nil) != want {
					t.Fatal("action-axis invocation DTO")
				}
				// Direct mutable DTO revalidation must reject the same invalid evidence.
				var raw PreflightResult
				if json.Unmarshal(b, &raw) != nil {
					t.Fatal("DTO")
				}
				if validPreflightResult(&raw) != want {
					t.Fatal("shared mutable DTO action guard")
				}
				v.Result = &raw
				if CheckPreflightBinding(&v, preflightIdentityBinding()) == nil {
					t.Fatal("failure became confirmable")
				}
			})
		}
	}
}
