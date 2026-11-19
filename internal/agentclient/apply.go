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

const (
	journalHeader         = "X-Dill-Journal-Id"
	journalCapacityHeader = "X-Dill-Journal-Capacity"
)

var ErrDeploymentUnknown = errors.New("deployment delivery unknown; look up the same request ID before taking further action")
var ErrJournalCapacity = errors.New("deployment journal capacity reached; preserve the journal and inspect agent state")

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
	return c.applyDeployment(ctx, epoch, stage, manifest, stack, selection, requestID, mode, "")
}

func (c *Client) ApplyJournalDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, requestID, mode, journal string) (DeploymentStatus, error) {
	if !canonicalHex(journal, 16) {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.applyDeployment(ctx, epoch, stage, manifest, stack, selection, requestID, mode, journal)
}

func (c *Client) applyDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, requestID, mode, journal string) (DeploymentStatus, error) {
	if (mode != "create" && mode != "update") || !canonicalHex(stage, 16) || !canonicalHex(manifest, 32) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, err := json.Marshal(map[string]string{"stageId": stage, "manifestSha256": manifest, "stack": stack, "selectionDigest": selection, "requestId": requestID, "confirm": mode})
	if err != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	path := "/v1/deploy/apply"
	if journal != "" {
		path = "/v1/deploy/journal-apply"
	}
	return c.deploymentStatus(ctx, epoch, requestID, stack, path, journal, body)
}

func (c *Client) RemoveDeployment(ctx context.Context, epoch, stack, selection, requestID, prior string) (DeploymentStatus, error) {
	if !canonicalHex(prior, 16) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, err := json.Marshal(map[string]string{"stack": stack, "selectionDigest": selection, "requestId": requestID, "priorRequestId": prior, "confirm": "remove"})
	if err != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.deploymentStatus(ctx, epoch, requestID, stack, "/v1/deploy/remove", "", body)
}

// DeploymentStatus looks up the same request without submitting a write.
func (c *Client) DeploymentStatus(ctx context.Context, epoch, requestID string) (DeploymentStatus, error) {
	return c.deploymentStatus(ctx, epoch, requestID, "", "", "", nil)
}

func (c *Client) JournalDeploymentStatus(ctx context.Context, epoch, id, stack, journal string) (DeploymentStatus, error) {
	return c.deploymentStatus(ctx, epoch, id, stack, "", journal, nil)
}

// FenceDeployment never submits or cancels an engine operation. Dill either
// durably forbids this request ID from admission or returns its existing status.
func (c *Client) FenceDeployment(ctx context.Context, epoch, id, stack, journal string) (DeploymentStatus, error) {
	if !canonicalHex(journal, 16) || stack == "" || len(stack) > 128 {
		return DeploymentStatus{}, ErrProtocol
	}
	body, err := json.Marshal(map[string]string{"stack": stack, "requestId": id, "confirm": "fence-if-unadmitted"})
	if err != nil {
		return DeploymentStatus{}, ErrProtocol
	}
	return c.deploymentStatus(ctx, epoch, id, stack, "/v1/deploy/fence", journal, body)
}

func (c *Client) deploymentStatus(ctx context.Context, epoch, id, stack, postPath, journal string, body []byte) (DeploymentStatus, error) {
	var empty DeploymentStatus
	session, err := c.httpsSession(epoch)
	if ctx == nil || err != nil || !canonicalHex(id, 16) || (journal != "" && !canonicalHex(journal, 16)) {
		return empty, ErrProtocol
	}
	method, path, limit, failure := http.MethodGet, "/v1/deploy/operations/"+id, 15*time.Second, ErrProtocol
	if body != nil {
		method, path, limit, failure = http.MethodPost, postPath, 6*time.Minute, ErrDeploymentUnknown
	}
	if postPath == "/v1/deploy/fence" {
		limit = 15 * time.Second
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
	if journal != "" {
		req.Header.Set(journalHeader, journal)
	}
	resp, err := c.do(req)
	if err != nil {
		return empty, failure
	}
	defer resp.Body.Close()
	if journal != "" {
		values := resp.Header.Values(journalHeader)
		if len(values) != 1 || values[0] != journal {
			return empty, failure
		}
	}
	if body != nil && resp.StatusCode == http.StatusInsufficientStorage && resp.Header.Get(journalCapacityHeader) == "low" {
		return empty, ErrJournalCapacity
	}
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
	case "not-admitted":
		if v.Effects == "none" && op == "" && (body == nil || postPath == "/v1/deploy/fence") {
			return v, nil
		}
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
