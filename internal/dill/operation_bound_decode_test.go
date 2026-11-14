package dill

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExplicitBoundInvocationDecoderIsolation(t *testing.T) {
	b, err := os.ReadFile("testdata/preflight-v1/apply-mismatch.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Stdout json.RawMessage `json:"stdout"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	var receipt OperationReceipt
	if err = json.Unmarshal(fixture.Stdout, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, d := range []OperationDiagnostic{
		{Code: "PREFLIGHT_MISMATCH", Message: "Reviewed resource selection changed. Review again before retrying."},
		{Code: "PREFLIGHT_UNAVAILABLE", Message: "Complete preflight evidence unavailable."},
	} {
		receipt.Errors = []OperationDiagnostic{d}
		payload, err := json.Marshal(OperationInvocation{Receipt: &receipt, ExitCode: 1, Status: "failed"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeOperationInvocation(payload); err == nil {
			t.Fatalf("legacy accepted %s", d.Code)
		}
		if _, err = DecodeBoundOperationInvocation(payload); err != nil {
			t.Fatalf("bound rejected %s: %v", d.Code, err)
		}
	}
}
