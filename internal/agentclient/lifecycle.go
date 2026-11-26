package agentclient

import (
	"context"
	"encoding/json"
	"strings"
)

func validLifecycleAction(action string) bool {
	return action == "start" || action == "stop" || action == "restart"
}

// PreviewLifecycle is journal-bound, read-only named preflight of the retained
// managed source. It cannot adopt arbitrary containers or override source files.
func (c *Client) PreviewLifecycle(ctx context.Context, epoch, stack, action, journal string) (*DeploymentPreview, error) {
	if !validLifecycleAction(action) || !canonicalHex(journal, 16) {
		return nil, ErrProtocol
	}
	return c.previewDeployment(ctx, epoch, stack, "/v1/deploy/"+action+"-preview", action, map[string]string{"stack": stack}, journal)
}
func (c *Client) ApplyLifecycle(ctx context.Context, epoch, stack, action, selection, request, prior, journal string) (DeploymentStatus, error) {
	if !validLifecycleAction(action) || !canonicalHex(journal, 16) || !canonicalHex(prior, 16) || request == prior || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, e := json.Marshal(map[string]string{"stack": stack, "selectionDigest": selection, "requestId": request, "priorRequestId": prior, "confirm": action})
	if e != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.deploymentStatus(ctx, epoch, request, stack, "/v1/deploy/"+action, journal, body)
}
