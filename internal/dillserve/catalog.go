package dillserve

import (
	"context"
	"encoding/hex"
	"encoding/json"
)

// Snapshot reads a staged manifest from the explicitly configured service. The
// caller must hold a writer-excluding configuration lease for this entire call.
// Health and status are assertions by the service, not attestation. The result
// does not guarantee that a stage remains unchanged after this GET, nor does a
// prior transfer authorize deployment. Never log this sensitive metadata.
func (c *Client) Snapshot(ctx context.Context, stageID string) (Manifest, error) {
	var zero Manifest
	id, err := hex.DecodeString(stageID)
	if ctx == nil || ctx.Err() != nil || err != nil || len(id) != 16 || hex.EncodeToString(id) != stageID {
		return zero, ErrUnavailable
	}
	h, err := c.Health(ctx)
	if err != nil {
		return zero, ErrUnavailable
	}
	supported := false
	for _, v := range h.Capabilities {
		if v == "snapshot-manifest" {
			supported = true
		}
	}
	if !supported {
		return zero, ErrUnavailable
	}
	if _, err = c.status(ctx); err != nil {
		return zero, ErrUnavailable
	}
	body, err := c.getLimit(ctx, "/v1/snapshots/"+stageID, 4<<20)
	if err != nil {
		return zero, ErrUnavailable
	}
	return ParseCatalog(ctx, stageID, body)
}

// ParseCatalog validates the fixed catalog envelope and its canonical manifest.
// It performs no transport authentication; callers must use a verified session.
func ParseCatalog(ctx context.Context, stageID string, body []byte) (Manifest, error) {
	var zero Manifest
	id, err := hex.DecodeString(stageID)
	if ctx == nil || ctx.Err() != nil || err != nil || len(id) != 16 || hex.EncodeToString(id) != stageID || len(body) > 4<<20 {
		return zero, ErrUnavailable
	}
	m, err := object(body, "stage_id", "manifest", "manifest_sha256")
	if err != nil || len(m) != 3 {
		return zero, ErrUnavailable
	}
	var got, digestText string
	if json.Unmarshal(m["stage_id"], &got) != nil || got != stageID || json.Unmarshal(m["manifest_sha256"], &digestText) != nil {
		return zero, ErrUnavailable
	}
	b, err := hex.DecodeString(digestText)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != digestText {
		return zero, ErrUnavailable
	}
	var digest [32]byte
	copy(digest[:], b)
	return validateManifest(ctx, m["manifest"], digest)
}
