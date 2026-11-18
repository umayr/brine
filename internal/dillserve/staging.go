package dillserve

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// ErrStageUnknown means publication may have occurred. Never retry automatically.
var ErrStageUnknown = errors.New("snapshot staging outcome unknown; operator inspection required")
var ErrStageRejected = errors.New("snapshot staging rejected; operator inspection may be required")

// Stage submits one already validated wire document. It does not establish consent.
// All malformed or interrupted responses are ambiguous, including cancellation.
func (c *Client) Stage(ctx context.Context, body []byte) (string, error) {
	return StageRequest(ctx, "http://dillserve", body, c.http.Do)
}

// StageRequest shares the snapshot wire contract with authenticated agent
// clients. The caller supplies the already verified transport and fixed origin.
func StageRequest(ctx context.Context, origin string, body []byte, send func(*http.Request) (*http.Response, error)) (string, error) {
	if ctx == nil || send == nil || len(body) == 0 || len(body) > 24<<20 || ctx.Err() != nil {
		return "", ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/v1/snapshots", bytes.NewReader(body))
	if err != nil {
		return "", ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	// Prevent net/http from replaying a request on connection failure.
	req.GetBody = nil
	resp, err := send(req)
	if err != nil {
		return "", ErrStageUnknown
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(b) > maxResponse {
		return "", ErrStageUnknown
	}
	if resp.StatusCode != http.StatusCreated {
		// Server-side errors and cancellation may follow publication or failed cleanup.
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusTooManyRequests:
			return "", ErrStageRejected
		default:
			return "", ErrStageUnknown
		}
	}
	m, err := object(b, "status", "stage_id")
	if err != nil || len(m) != 2 {
		return "", ErrStageUnknown
	}
	var status, id string
	if json.Unmarshal(m["status"], &status) != nil || json.Unmarshal(m["stage_id"], &id) != nil || status != "staged" || len(id) != 32 {
		return "", ErrStageUnknown
	}
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 || hex.EncodeToString(raw) != id {
		return "", ErrStageUnknown
	}
	return id, nil
}
