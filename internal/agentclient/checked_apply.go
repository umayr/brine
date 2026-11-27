package agentclient

import (
	"context"
	"encoding/json"
	"strings"
)

// ApplyCheckedJournalDeployment can update only the exact managed predecessor
// from a fresh qualified preview. No create fallback, implicit retry or review.
func (c *Client) ApplyCheckedJournalDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, request, prior, journal string) (DeploymentStatus, error) {
	if !canonicalHex(stage, 16) || !canonicalHex(manifest, 32) || !canonicalHex(request, 16) || !canonicalHex(prior, 16) || request == prior || !canonicalHex(journal, 16) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, _ := json.Marshal(map[string]string{"stageId": stage, "manifestSha256": manifest, "stack": stack, "selectionDigest": selection, "requestId": request, "priorRequestId": prior, "confirm": "update"})
	return c.deploymentStatus(ctx, epoch, request, stack, "/v1/deploy/checked-apply", journal, body)
}
