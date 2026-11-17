package remoteui

import (
	"testing"

	"github.com/umayr/brine/internal/agentclient"
)

func TestAgentMappingInspectionAndInventory(t *testing.T) {
	inventory := mapInventory([]agentclient.InventoryItem{{ID: testID, Name: "n", Stack: "stack", Service: "service", State: "running", Image: "image"}})
	if len(inventory) != 1 || inventory[0].ID != testID || inventory[0].Service != "service" {
		t.Fatal(inventory)
	}
	var r agentclient.InspectResult
	r.ContainerID = testID
	r.Inspect.ID = testID
	r.Stack = "stack"
	r.Service = "service"
	r.ObservedAt = "2025-01-01T00:00:00Z"
	r.Inspect.EnvKeys = []string{"TOKEN"}
	r.Inspect.Labels = map[string]string{"dill.managed": "true"}
	r.Inspect.Ports = []agentclient.Port{{Protocol: "tcp", ContainerPort: 80}}
	r.Inspect.Mounts = []agentclient.Mount{{Destination: "/app", ReadOnly: true}}
	result, err := mapInspection(r, testID)
	if err != nil || result.ID != testID || result.Stack != "stack" || result.ObservedAt != r.ObservedAt || len(result.EnvKeys) != 1 || result.EnvKeys[0] != "TOKEN" || len(result.Ports) != 1 || result.Ports[0].ContainerPort != 80 || len(result.Mounts) != 1 || !result.Mounts[0].ReadOnly {
		t.Fatal(result, err)
	}
	r.Inspect.ID = otherID
	if _, err = mapInspection(r, testID); err == nil {
		t.Fatal("mismatched ID accepted")
	}
}
