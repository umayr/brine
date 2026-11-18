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
	CreateEnabled bool
}

// PreviewDeployment asks the authenticated agent to run Dill's qualified
// preflight on the exact catalogued snapshot. It does not submit engine writes
// or grant permission to apply the returned selection.
func (c *Client) PreviewDeployment(ctx context.Context, epoch, stageID, manifestDigest, stack string) (*DeploymentPreview, error) {
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil || !canonicalHex(stageID, 16) || !canonicalHex(manifestDigest, 32) || len(stack) == 0 || len(stack) > dill.PreflightMaxNameBytes {
		return nil, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, 32*time.Second)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	body, err := json.Marshal(struct {
		StageID  string `json:"stageId"`
		Manifest string `json:"manifestSha256"`
		Stack    string `json:"stack"`
	}{stageID, manifestDigest, stack})
	if err != nil {
		return nil, ErrProtocol
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/deploy/preview", bytes.NewReader(body))
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
	if err != nil || result.Outcome != "succeeded" || result.Operation != "up" || result.Stack != stack || result.Target == nil || result.Target.Engine != c.binding.EngineKind ||
		result.Target.EndpointDigest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(c.binding.EngineSocket))) || session.Err() != nil || ctx.Err() != nil {
		return nil, ErrProtocol
	}
	return &DeploymentPreview{PreflightResult: result, CreateEnabled: resp.Header.Get("X-Dill-Create-Enabled") == "1"}, nil
}

func canonicalHex(s string, size int) bool {
	if len(s) != size*2 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(raw) == s
}
