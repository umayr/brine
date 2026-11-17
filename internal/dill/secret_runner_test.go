//go:build unix

package dill

import (
	"strings"
	"testing"
)

const testBinding = `{"version":"dill.secret-bindings/1","target":"lab-a","stack":"demo","bindings":[{"name":"db","source":"db-prod","revision":"r1"}]}`

func TestSecretPlanAndProofStrictness(t *testing.T) {
	proof := `{"contract":"dill.alpha-secrets-preview/1","proof":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"}`
	for _, v := range []string{proof + `{}`, strings.Replace(proof, `"proof":`, `"proof":null,"proof":`, 1), strings.Replace(proof, `AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE`, `short`, 1)} {
		if _, err := parseProof([]byte(v)); err == nil {
			t.Fatal("invalid proof accepted")
		}
	}
	for _, v := range []string{`{}`, `{"changes":[],"summary":null}`, `{"changes":[],"summary":{}} trailing`, `{"changes":[],"summary":{}}`} {
		if _, err := parseSecretPlan([]byte(v), 1); err == nil {
			t.Fatal("invalid plan accepted")
		}
		if _, err := parseSecretPlan([]byte(v), 0); err == nil {
			t.Fatal("invalid no-drift plan accepted")
		}
	}
}
