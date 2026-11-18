package agentclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/umayr/brine/internal/dill"
)

type DeploymentPreview struct {
	*dill.PreflightResult
	CreateEnabled                   bool
	Mode, PriorRequestID, JournalID string
}

// PreviewDeployment asks the authenticated agent to run Dill's qualified
// preflight on the exact catalogued snapshot. It does not submit engine writes
// or grant permission to apply the returned selection.
func (c *Client) PreviewDeployment(ctx context.Context, epoch, stageID, manifestDigest, stack string) (*DeploymentPreview, error) {
	if !canonicalHex(stageID, 16) || !canonicalHex(manifestDigest, 32) {
		return nil, ErrProtocol
	}
	return c.previewDeployment(ctx, epoch, stack, "/v1/deploy/preview", "up", map[string]string{"stageId": stageID, "manifestSha256": manifestDigest, "stack": stack})
}

func (c *Client) PreviewRemoval(ctx context.Context, epoch, stack string) (*DeploymentPreview, error) {
	return c.previewDeployment(ctx, epoch, stack, "/v1/deploy/remove-preview", "down", map[string]string{"stack": stack})
}

func (c *Client) previewDeployment(ctx context.Context, epoch, stack, path, action string, fields map[string]string) (*DeploymentPreview, error) {
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil || len(stack) == 0 || len(stack) > dill.PreflightMaxNameBytes {
		return nil, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, 32*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, ErrProtocol
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = nil // No implicit replay of target coordination metadata writes.
	resp, err := c.do(req)
	if err != nil {
		return nil, ErrProtocol
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		return nil, ErrProtocol
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, dill.PreflightMaxOutputBytes+1))
	if err != nil || len(raw) > dill.PreflightMaxOutputBytes {
		return nil, ErrProtocol
	}
	result, err := dill.DecodePreflightResult(raw)
	if err != nil || result.Outcome != "succeeded" || result.Operation != action || result.Stack != stack || result.Target == nil || result.Target.Engine != c.binding.EngineKind ||
		result.Target.EndpointDigest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(c.binding.EngineSocket))) || session.Err() != nil || ctx.Err() != nil {
		return nil, ErrProtocol
	}
	mode, prior := resp.Header.Get("X-Dill-Deployment-Mode"), resp.Header.Get("X-Dill-Prior-Request")
	if action == "down" {
		if mode != "remove" || !canonicalHex(prior, 16) {
			return nil, ErrProtocol
		}
	} else {
		if prior != "" || (mode != "" && mode != "create" && mode != "update") {
			return nil, ErrProtocol
		}
		if mode == "" && resp.Header.Get("X-Dill-Create-Enabled") == "1" {
			mode = "create"
		}
	}
	j := resp.Header.Values(journalHeader)
	if len(j) > 1 || (len(j) == 1 && !canonicalHex(j[0], 16)) {
		return nil, ErrProtocol
	}
	return &DeploymentPreview{PreflightResult: result, CreateEnabled: mode == "create", Mode: mode, PriorRequestID: prior, JournalID: resp.Header.Get(journalHeader)}, nil
}

func canonicalHex(s string, size int) bool {
	if len(s) != size*2 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(raw) == s
}
