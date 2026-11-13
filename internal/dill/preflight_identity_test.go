package dill

import (
	"encoding/json"
	"strings"
	"testing"
)

// Two independently named present containers cannot both be authoritative
// inventory entries for one immutable container ID. Names are not collapsed.
func preflightTwoContainerResult(t *testing.T) map[string]any {
	t.Helper()
	m := preflightBase(t)
	first := preflightRow(m, 0)
	first["presence"] = "present"
	first["identityKind"] = "immutable-id"
	first["identity"] = strings.Repeat("a", 64)
	first["action"] = "ensure-started"
	second := map[string]any{}
	for k, v := range first {
		second[k] = v
	}
	second["name"] = "synthetic_worker"
	second["identity"] = strings.Repeat("b", 64)
	network := preflightRow(m, 1)
	network["presence"] = "present"
	network["identityKind"] = "immutable-id"
	// Container and network identity namespaces are independent.
	network["identity"] = first["identity"]
	volume := preflightRow(m, 2)
	volume["action"] = "retain"
	m["resources"] = []any{first, second, network, volume}
	preflightScope(m, "containers")["count"] = 2
	preflightScope(m, "containers")["presentCount"] = 2
	preflightScope(m, "network")["presentCount"] = 1
	return m
}

func preflightIdentityBinding() PreflightBinding {
	return PreflightBinding{
		Stack: "synthetic", Operation: "up", Engine: "docker",
		EndpointDigest:          "sha256:" + strings.Repeat("1", 64),
		QualifiedIdentityDigest: "sha256:" + strings.Repeat("2", 64),
	}
}

func TestPreflightRejectDuplicatePresentContainerIDs(t *testing.T) {
	for _, seam := range []string{"decoder", "invocation", "invocation-decoder", "trusted-binding"} {
		t.Run(seam, func(t *testing.T) {
			m := preflightTwoContainerResult(t)
			baseline := preflightJSON(t, m)
			r, err := DecodePreflightResult(baseline)
			if err != nil || r == nil {
				t.Fatal("invalid distinct-ID/cross-kind baseline", err)
			}
			zero := 0
			v := ObservePreflightInvocation(baseline, &zero)
			if v.Result == nil || v.Status != "succeeded" || CheckPreflightBinding(&v, preflightIdentityBinding()) != nil {
				t.Fatal("valid baseline was not accepted across all seams")
			}
			preflightRow(m, 1)["identity"] = preflightRow(m, 0)["identity"]
			b := preflightJSON(t, m)
			switch seam {
			case "decoder":
				if r, err := DecodePreflightResult(b); r != nil || err != ErrPreflightContract {
					t.Fatal("decoder accepted two names for one present container ID")
				}
			case "invocation":
				observed := ObservePreflightInvocation(b, &zero)
				if observed.Result != nil || observed.Status != "unknown" {
					t.Fatal("observer promoted duplicate container identity evidence")
				}
			case "invocation-decoder":
				local := map[string]any{"result": json.RawMessage(b), "processExit": 0, "status": "succeeded"}
				if got, err := DecodePreflightInvocation(preflightJSON(t, local)); got != nil || err != ErrPreflightContract {
					t.Fatal("invocation decoder accepted duplicate container identity evidence")
				}
			case "trusted-binding":
				// Bypass wire decoding to exercise the shared DTO revalidation gate.
				var raw PreflightResult
				if err := json.Unmarshal(b, &raw); err != nil {
					t.Fatal(err)
				}
				v.Result = &raw
				if CheckPreflightBinding(&v, preflightIdentityBinding()) != ErrPreflightBinding {
					t.Fatal("trusted binding accepted duplicate present container IDs")
				}
			}
		})
	}
}

func TestPreflightDistinctContainerIDsAndAbsentReferencesRemainValid(t *testing.T) {
	for _, mode := range []string{"distinct-live", "absent-references", "mixed-live-planned-namespace"} {
		m := preflightTwoContainerResult(t)
		if mode == "absent-references" {
			for i := 0; i < 2; i++ {
				x := preflightRow(m, i)
				x["presence"] = "absent"
				x["identityKind"] = "planned-name"
				x["identity"] = x["name"]
				x["action"] = "create"
			}
			preflightScope(m, "containers")["presentCount"] = 0
		} else if mode == "mixed-live-planned-namespace" {
			// A planned name can coincidentally spell a present ID. It is
			// a namespace reference, not another immutable incarnation claim.
			x := preflightRow(m, 1)
			x["name"] = preflightRow(m, 0)["identity"]
			x["identity"] = x["name"]
			x["presence"] = "absent"
			x["identityKind"] = "planned-name"
			x["action"] = "create"
			preflightScope(m, "containers")["presentCount"] = 1
		}
		b := preflightJSON(t, m)
		if r, err := DecodePreflightResult(b); err != nil || r == nil || len(r.Resources) != 4 {
			t.Fatal("valid identities were collapsed or rejected", err)
		}
		zero := 0
		v := ObservePreflightInvocation(b, &zero)
		if v.Result == nil || CheckPreflightBinding(&v, preflightIdentityBinding()) != nil {
			t.Fatal("valid identities failed process/context binding")
		}
	}
}
