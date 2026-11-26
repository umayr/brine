package agentclient

import (
	"strings"
	"testing"
)

func TestCapabilitiesDocumentPreservesCredentialScopes(t *testing.T) {
	body := []byte(`{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities","inventory"],"deployment_grants":["stage"]}`)
	caps, e := parseCapabilities(body)
	if e != nil || strings.Join(caps, ",") != "monitor.session,monitor.capabilities,monitor.inventory,deployment.stage" {
		t.Fatal(caps, e)
	}
	for _, bad := range []string{`{}`, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities"],"deployment_grants":null}`, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities","session"],"deployment_grants":[]}`, `{"schema":"dill.agent.capabilities.v1","monitor":["session","capabilities"],"deployment_grants":["*"]}`} {
		if _, e = parseCapabilities([]byte(bad)); e == nil {
			t.Fatal("invalid capability document accepted")
		}
	}
}
