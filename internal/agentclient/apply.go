package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrDeploymentUnknown = errors.New("deployment delivery unknown; look up the same request ID before taking further action")

type DeploymentStatus struct {
	RequestID   string `json:"requestId"`
	Stack       string `json:"stack"`
	State       string `json:"state"`
	Effects     string `json:"effects"`
	OperationID string `json:"operationId,omitempty"`
}

func (c *Client) CreateDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, requestID string) (DeploymentStatus, error) {
	return c.ApplyDeployment(ctx, epoch, stage, manifest, stack, selection, requestID, "create")
}

func (c *Client) ApplyDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, requestID, mode string) (DeploymentStatus, error) {
	if (mode != "create" && mode != "update") || !canonicalHex(stage, 16) || !canonicalHex(manifest, 32) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, err := json.Marshal(map[string]string{"stageId": stage, "manifestSha256": manifest, "stack": stack, "selectionDigest": selection, "requestId": requestID, "confirm": mode})
	if err != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.deploymentStatus(ctx, epoch, requestID, stack, "/v1/deploy/apply", body)
}

func (c *Client) RemoveDeployment(ctx context.Context, epoch, stack, selection, requestID, prior string) (DeploymentStatus, error) {
	if !canonicalHex(prior, 16) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, err := json.Marshal(map[string]string{"stack": stack, "selectionDigest": selection, "requestId": requestID, "priorRequestId": prior, "confirm": "remove"})
	if err != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.deploymentStatus(ctx, epoch, requestID, stack, "/v1/deploy/remove", body)
}

// DeploymentStatus looks up the same request without submitting a write.
func (c *Client) DeploymentStatus(ctx context.Context, epoch, requestID string) (DeploymentStatus, error) {
	return c.deploymentStatus(ctx, epoch, requestID, "", "", nil)
}

func (c *Client) deploymentStatus(ctx context.Context, epoch, id, stack, postPath string, body []byte) (DeploymentStatus, error) {
	var empty DeploymentStatus
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil || !canonicalHex(id, 16) {
		return empty, ErrProtocol
	}
	method, path, limit, failure := http.MethodGet, "/v1/deploy/operations/"+id, 15*time.Second, ErrProtocol
	if body != nil {
		method, path, limit, failure = http.MethodPost, postPath, 6*time.Minute, ErrDeploymentUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return empty, failure
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.GetBody = nil
	}
	resp, err := c.do(req)
	if err != nil {
		return empty, failure
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		return empty, failure
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(b) > 4096 || checkJSON(b) != nil || ctx.Err() != nil || session.Err() != nil {
		return empty, failure
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var v DeploymentStatus
	if d.Decode(&v) != nil || v.RequestID != id || v.Stack == "" || len(v.Stack) > 128 || (stack != "" && v.Stack != stack) {
		return empty, failure
	}
	op := v.OperationID
	if op != "" && (!strings.HasPrefix(op, "op_") || !canonicalHex(strings.TrimPrefix(op, "op_"), 16)) {
		return empty, failure
	}
	switch v.State {
	case "running", "unknown":
		if v.Effects == "unknown" && op == "" {
			return v, nil
		}
	case "succeeded":
		if v.Effects == "complete" && op != "" {
			return v, nil
		}
	case "failed":
		if v.Effects == "none" || (v.Effects == "partial" && op != "") {
			return v, nil
		}
	}
	return empty, failure
}
