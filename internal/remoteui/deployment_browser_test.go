package remoteui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
)

// TestDeploymentStatusBrowserFixture renders the real status template with
// synthetic results; the handler tests exercise lookup and admission.
func TestDeploymentStatusBrowserFixture(t *testing.T) {
	dir := os.Getenv("BRINE_DEPLOYMENT_STATUS_FIXTURE")
	if dir == "" {
		t.Skip("browser fixture only")
	}
	const id = "1a23456789abcdef0123456789abcdef"
	unverified := deploymentPage{Host: "demo", ID: id}
	unverified.showGenericStatus(agentclient.DeploymentStatus{RequestID: id, Stack: "unverified-stack", State: "not-admitted", Effects: "none"})
	for name, page := range map[string]deploymentPage{
		"recover":     {Host: "demo", ID: id, CSRF: "synthetic-csrf", ManualRecover: true, Error: deploymentStatusUnavailable},
		"unverified":  unverified,
		"unavailable": {Host: "demo", ID: id, Error: deploymentStatusUnavailable},
		"partial": {Host: "demo", ID: id, Status: &agentclient.DeploymentStatus{
			RequestID: id, Stack: "synthetic", State: "failed", Effects: "partial", OperationID: "op_" + id,
		}},
		"unknown": {Host: "demo", ID: id, Status: &agentclient.DeploymentStatus{
			RequestID: id, Stack: "synthetic", State: "unknown", Effects: "unknown",
		}},
	} {
		f, err := os.Create(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		renderErr := deploymentView.Execute(f, page)
		closeErr := f.Close()
		if renderErr != nil || closeErr != nil {
			t.Fatalf("write fixture: render %v, close %v", renderErr, closeErr)
		}
	}
}
