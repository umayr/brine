package dill

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Phase expectations are an explicit allow-table derived from the t27/t51 A–F
// clarification of frozen r4, not from production catalog/validation helpers.
func TestPreflightLiteralFailurePhaseCrossProduct(t *testing.T) {
	cleanupSets := [][]string{
		{}, {"ENGINE_CLOSE_FAILED"}, {"COORDINATION_COMMIT_FAILED"}, {"COORDINATION_RELEASE_FAILED"},
		{"ENGINE_CLOSE_FAILED", "COORDINATION_COMMIT_FAILED"}, {"ENGINE_CLOSE_FAILED", "COORDINATION_RELEASE_FAILED"},
		{"COORDINATION_COMMIT_FAILED", "COORDINATION_RELEASE_FAILED"},
		{"ENGINE_CLOSE_FAILED", "COORDINATION_COMMIT_FAILED", "COORDINATION_RELEASE_FAILED"},
	}
	message := map[string]string{}
	for _, entry := range preflightFrozenDiagnostics {
		message[entry.code] = entry.message
	}
	allowed := func(code string, cleanup int, target bool, outcome, coord string) bool {
		// Cleanup index 1 is close-only; every higher index includes state cleanup.
		terminal := false
		if cleanup == 1 {
			terminal = outcome == "unknown" && (coord == "none" || coord == "unknown")
		}
		if cleanup >= 2 {
			terminal = outcome == "unknown" && coord == "unknown"
		}
		switch code {
		case "INVALID_ARGUMENTS", "UNSUPPORTED_OPERATION", "UNSUPPORTED_CONTRACT", "UNSUPPORTED_FORMAT", "INVALID_TARGET", "UNSUPPORTED_PREFLIGHT":
			return cleanup == 0 && !target && outcome == "invalid_input" && coord == "none"
		case "EVALUATION_FAILED", "QUALIFICATION_FAILED":
			return !target && coord == "none" && ((cleanup == 0 && outcome == "failed") || (cleanup == 1 && outcome == "unknown"))
		case "CONFIG_INVALID":
			return terminal || (cleanup == 0 && ((outcome == "invalid_input" && coord == "none") || (outcome == "failed" && coord == "written")))
		case "SECRET_UNAVAILABLE":
			return !target && (terminal || (cleanup == 0 && outcome == "failed" && (coord == "none" || coord == "written")))
		case "CANCELLED":
			return terminal || (cleanup == 0 && outcome == "cancelled" && (coord == "none" || coord == "written"))
		case "COORDINATION_UNAVAILABLE":
			return terminal || (cleanup == 0 && (outcome == "failed" || outcome == "unknown") && coord == "unknown")
		case "PREFLIGHT_UNAVAILABLE", "PREFLIGHT_LIMIT_EXCEEDED":
			return terminal || (cleanup == 0 && outcome == "failed" && (coord == "none" || coord == "written"))
		case "":
			return cleanup != 0 && terminal
		}
		return false
	}
	base := preflightFixtureRead(t, "preview-limit-failure.json")
	target := map[string]any{"engine": "docker", "endpointDigest": "sha256:" + strings.Repeat("1", 64), "qualifiedIdentityDigest": "sha256:" + strings.Repeat("2", 64)}
	primary := []string{""}
	for _, entry := range preflightFrozenDiagnostics {
		if entry.code != "ENGINE_CLOSE_FAILED" && entry.code != "COORDINATION_COMMIT_FAILED" && entry.code != "COORDINATION_RELEASE_FAILED" {
			primary = append(primary, entry.code)
		}
	}
	mismatches, accepted, rejected := 0, 0, 0
	for _, code := range primary {
		for cleanup, extra := range cleanupSets {
			if code == "" && cleanup == 0 {
				continue
			}
			for _, hasTarget := range []bool{false, true} {
				for _, coord := range []string{"none", "written", "unknown"} {
					for _, outcome := range []string{"succeeded", "invalid_input", "failed", "cancelled", "unknown"} {
						var m map[string]any
						if json.Unmarshal(base.Stdout, &m) != nil {
							t.Fatal("fixture")
						}
						m["target"] = nil
						if hasTarget {
							m["target"] = target
						}
						m["stack"] = "synthetic"
						if code == "SECRET_UNAVAILABLE" {
							m["stack"] = ""
						}
						m["coordinationEffects"] = coord
						m["outcome"] = outcome
						m["resourcesTruncated"] = code == "PREFLIGHT_LIMIT_EXCEEDED"
						es := []any{}
						if code != "" {
							es = append(es, map[string]any{"code": code, "message": message[code]})
						}
						for _, c := range extra {
							es = append(es, map[string]any{"code": c, "message": message[c]})
						}
						m["errors"] = es
						b := preflightJSON(t, m)
						want := allowed(code, cleanup, hasTarget, outcome, coord)
						r, e := DecodePreflightResult(b)
						got := e == nil && r != nil
						label := fmt.Sprintf("code=%s cleanup=%d target=%t outcome=%s coord=%s", code, cleanup, hasTarget, outcome, coord)
						if got != want {
							if mismatches < 8 {
								t.Errorf("phase result mismatch %s got=%t want=%t", label, got, want)
							}
							mismatches++
							continue
						}
						if want {
							accepted++
						} else {
							rejected++
						}
						for _, exit := range []int{0, 1, 2} {
							wantExit := 1
							if outcome == "invalid_input" {
								wantExit = 2
							}
							wantResult := want && exit == wantExit
							inv := ObservePreflightInvocation(b, &exit)
							if (inv.Result != nil) != wantResult || (!wantResult && inv.Status != "unknown") {
								t.Fatalf("phase invocation mismatch %s exit=%d", label, exit)
							}
							if CheckPreflightBinding(&inv, preflightIdentityBinding()) == nil {
								t.Fatalf("failure became confirmable %s", label)
							}
							status := "unknown"
							if wantResult {
								status = inv.Status
							}
							local := map[string]any{"result": json.RawMessage(b), "processExit": exit, "status": status}
							decoded, err := DecodePreflightInvocation(preflightJSON(t, local))
							if (err == nil && decoded != nil && decoded.Result != nil) != wantResult {
								t.Fatalf("phase DTO mismatch %s exit=%d", label, exit)
							}
						}
					}
				}
			}
		}
	}
	if mismatches != 0 {
		t.Fatalf("phase cross-product mismatches=%d accepted=%d rejected=%d", mismatches, accepted, rejected)
	}
	if accepted == 0 || rejected == 0 {
		t.Fatal("ineffective phase table")
	}
	t.Logf("literal phase cross-product accepted=%d rejected=%d", accepted, rejected)
}

func TestPreflightLateSecretFailureStillClearsIdentity(t *testing.T) {
	f := preflightFixtureRead(t, "preview-limit-failure.json")
	var m map[string]any
	_ = json.Unmarshal(f.Stdout, &m)
	m["outcome"] = "failed"
	m["coordinationEffects"] = "written"
	m["target"] = nil
	m["stack"] = ""
	m["resourcesTruncated"] = false
	m["errors"] = []any{map[string]any{"code": "SECRET_UNAVAILABLE", "message": "Secret configuration or source unavailable."}}
	baseline := preflightJSON(t, m)
	if _, e := DecodePreflightResult(baseline); e != nil {
		t.Fatal("valid late secret failure", e)
	}
	m["stack"] = "synthetic"
	preflightReject(t, preflightJSON(t, m))
}
