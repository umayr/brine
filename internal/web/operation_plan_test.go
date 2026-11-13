package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
)

func TestQualifiedWebReadsDenyUnauthenticatedAndStdinFormatting(t *testing.T) {
	r := dill.New("/must-not-execute")
	r.Profile = "alpha"
	r.QualifiedOperation = &dill.QualifiedOperationConfig{}
	h := &Handler{agent: agent.NewLocalAgent(nil, r)}
	for _, route := range []struct {
		handler http.HandlerFunc
		want    int
	}{{h.stackPlan, 401}, {h.stackValidate, 401}, {h.stackFmt, 422}} {
		w := httptest.NewRecorder()
		route.handler(w, httptest.NewRequest("POST", "http://127.0.0.1/", nil))
		if w.Code != route.want {
			t.Fatalf("qualified route bypassed gate: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOperationPlanRendersReceivedDigestSummaryNotFabricatedDiff(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	result := &dill.PlanResult{Operation: &dill.OperationReceipt{
		Version: "dill.operation/1", OperationID: "op_" + strings.Repeat("a", 32), Operation: "plan", Stack: "synthetic", Outcome: "changes", Effects: "none", ChangesDetected: true,
		Target:    &dill.OperationTarget{Engine: "docker", EndpointDigest: "sha256:" + strings.Repeat("b", 64)},
		Resources: []dill.OperationResource{{Kind: "service", IdentityDigest: "sha256:" + strings.Repeat("c", 64), Action: "create", Effect: "none"}}, ResourcesTruncated: true,
		Errors: []dill.OperationDiagnostic{{Code: "SYNTHETIC", Message: "<script>never</script>"}},
	}, Summary: dill.PlanSummary{Create: 1}, Changes: []dill.PlanChange{{Service: "MUST_NOT_FABRICATE_SERVICE", Kind: "create", Diffs: []dill.FieldDiff{{Field: "RAW_DIFF_SENTINEL"}}}}}
	var out bytes.Buffer
	if err = ts.partials["plan-diff"].Execute(&out, result); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, want := range []string{"Read-only operation plan", "Operation ID: op_", "Outcome: changes", "Effects: none", "Endpoint selector digest (not daemon identity)", "Qualified identity: not received", "Resource observations truncated", "1 create", "sha256:" + strings.Repeat("c", 64), "&lt;script&gt;"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %s", want, html)
		}
	}
	for _, bad := range []string{"MUST_NOT_FABRICATE_SERVICE", "RAW_DIFF_SENTINEL", "<script>never</script>", "No changes — stack is up to date"} {
		if strings.Contains(html, bad) {
			t.Errorf("fabricated/unsafe %q", bad)
		}
	}
}
