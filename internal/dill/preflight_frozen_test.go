package dill

import (
	"encoding/json"
	"strings"
	"testing"
)

// Literal transcription from frozen revision 4, independent of the production
// catalog. Do not generate expected strings from preflightMessages.
var preflightFrozenDiagnostics = []struct{ code, message, outcome, coordination string }{
	{"INVALID_ARGUMENTS", "Invalid preflight arguments.", "invalid_input", "none"},
	{"UNSUPPORTED_OPERATION", "Unsupported preflight operation.", "invalid_input", "none"},
	{"UNSUPPORTED_CONTRACT", "Unsupported preflight contract.", "invalid_input", "none"},
	{"UNSUPPORTED_FORMAT", "Unsupported preflight format.", "invalid_input", "none"},
	{"INVALID_TARGET", "Explicit local Docker target and stack are required.", "invalid_input", "none"},
	{"UNSUPPORTED_PREFLIGHT", "Unsupported qualified preflight invocation.", "invalid_input", "none"},
	{"SECRET_UNAVAILABLE", "Secret configuration or source unavailable.", "failed", "none"},
	{"EVALUATION_FAILED", "Configuration evaluation failed.", "failed", "none"},
	{"CONFIG_INVALID", "Configuration is invalid.", "invalid_input", "none"},
	{"QUALIFICATION_FAILED", "Qualified target unavailable.", "failed", "none"},
	{"COORDINATION_UNAVAILABLE", "Trusted coordination state unavailable. Inspect before retrying.", "unknown", "unknown"},
	{"PREFLIGHT_UNAVAILABLE", "Complete preflight evidence unavailable.", "failed", "written"},
	{"PREFLIGHT_LIMIT_EXCEEDED", "Preflight response exceeds supported limits.", "failed", "written"},
	{"CANCELLED", "Preflight cancelled or deadline exceeded.", "cancelled", "written"},
	{"ENGINE_CLOSE_FAILED", "Preflight transport cleanup failed.", "unknown", "unknown"},
	{"COORDINATION_COMMIT_FAILED", "Durable preflight outcome unavailable. Inspect before retrying.", "unknown", "unknown"},
	{"COORDINATION_RELEASE_FAILED", "Preflight coordination cleanup failed. Inspect before retrying.", "unknown", "unknown"},
}

func TestPreflightFrozenLiteralDiagnostics(t *testing.T) {
	if len(preflightFrozenDiagnostics) != 17 {
		t.Fatal("frozen test catalog")
	}
	for _, entry := range preflightFrozenDiagnostics {
		t.Run(entry.code, func(t *testing.T) {
			f := preflightFixtureRead(t, "preview-limit-failure.json")
			var m map[string]any
			if json.Unmarshal(f.Stdout, &m) != nil {
				t.Fatal("failure baseline")
			}
			m["outcome"] = entry.outcome
			m["coordinationEffects"] = entry.coordination
			m["resourcesTruncated"] = entry.code == "PREFLIGHT_LIMIT_EXCEEDED"
			// No stage-provenance assertion is inferred for these synthetic failures.
			// Their conservative null target is valid before/after the relevant stages.
			m["target"] = nil
			m["stack"] = ""
			m["errors"] = []any{map[string]any{"code": entry.code, "message": entry.message}}
			b := preflightJSON(t, m)
			if r, e := DecodePreflightResult(b); e != nil || r == nil {
				t.Fatal("rejected exact frozen diagnostic", e)
			}
			exit := 1
			if entry.outcome == "invalid_input" {
				exit = 2
			}
			if v := ObservePreflightInvocation(b, &exit); v.Result == nil || v.Status == "succeeded" {
				t.Fatal("lost conservative frozen invocation")
			}
			for _, bad := range []string{entry.message + " ", strings.TrimSuffix(entry.message, "."), "private SDK sentinel"} {
				m["errors"] = []any{map[string]any{"code": entry.code, "message": bad}}
				preflightReject(t, preflightJSON(t, m))
			}
			m["errors"] = []any{map[string]any{"code": entry.code + "_UNKNOWN", "message": entry.message}}
			preflightReject(t, preflightJSON(t, m))
		})
	}
	// Explicit old buggy spelling must remain a rejection, not an accepted alias.
	f := preflightFixtureRead(t, "preview-metadata-unknown.json")
	var m map[string]any
	_ = json.Unmarshal(f.Stdout, &m)
	m["errors"] = []any{map[string]any{"code": "COORDINATION_RELEASE_FAILED", "message": "Preflight coordination cleanup failed."}}
	preflightReject(t, preflightJSON(t, m))
}

func TestPreflightFrozenRawStackAliases(t *testing.T) {
	for _, verb := range []string{"preflight", "operation"} {
		a, e := BuildPreflightArguments(preflightArgsBase())
		if e != nil {
			t.Fatal(e)
		}
		if verb == "operation" {
			a, e = BuildBoundPreflightArguments(preflightArgsBase(), "sha256:"+strings.Repeat("a", 64))
			if e != nil {
				t.Fatal(e)
			}
		}
		// Every frozen raw alias supports both dash and string-value forms.
		for _, alias := range []string{"p", "stack-name", "project-name"} {
			for _, dash := range []string{"-", "--"} {
				for _, equals := range []bool{false, true} {
					b := preflightRawStackAlias(a, dash+alias, equals)
					if ValidatePreflightArguments(b) != nil {
						t.Fatalf("rejected frozen alias %s%s equals=%t", dash, alias, equals)
					}
				}
			}
		}
		for _, dash := range []string{"-", "--"} {
			for _, equals := range []bool{false, true} {
				t.Run(verb+"/"+dash+"stack/"+map[bool]string{false: "separate", true: "equals"}[equals], func(t *testing.T) {
					if ValidatePreflightArguments(a) != nil {
						t.Fatal("invalid canonical baseline")
					}
					if ValidatePreflightArguments(preflightRawStackAlias(a, dash+"stack", equals)) != ErrPreflightArguments {
						t.Fatal("accepted internal semantic key as raw alias")
					}
				})
			}
		}
		// Flag-looking substrings inside legitimate values remain uninterpreted.
		for _, value := range []string{"/private/--stack/synthetic.pkl", "literal -stack text"} {
			if ValidatePreflightArguments(preflightArgsReplace(a, "-f", value)) != nil {
				t.Fatal("scanned value for alias text")
			}
		}
	}
}
func preflightRawStackAlias(a []string, alias string, equals bool) []string {
	b := append([]string{}, a...)
	for i := range b {
		if b[i] == "-p" {
			if equals {
				return append(append(b[:i:i], alias+"="+b[i+1]), b[i+2:]...)
			}
			b[i] = alias
			return b
		}
	}
	return b
}
