package agentclient

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/umayr/brine/internal/dillserve"
)

// OperatorTarget is the exact non-secret identity covered by a local approval.
// This is not a signed association between a deploy bearer and Dill's engine.
type OperatorTarget struct {
	Address, LeafSHA256, AgentPublicKey, EngineID, EngineKind, EngineSocket, DaemonID string
	Generation                                                                        uint64
}

// MatchesOperatorTarget checks the complete approved HTTPS origin and signed
// monitor identity against the active pairing. It does not contact Dill.
func (p *Pairing) MatchesOperatorTarget(reviewed OperatorTarget, address string) bool {
	if p == nil || !ValidHTTPSAddress(address) {
		return false
	}
	b := p.binding
	return reviewed == (OperatorTarget{Address: address, LeafSHA256: p.pin, AgentPublicKey: hex.EncodeToString(b.AgentPublicKey),
		EngineID: b.EngineID, EngineKind: b.EngineKind, EngineSocket: b.EngineSocket, DaemonID: b.DaemonID, Generation: b.Generation})
}

type LocalDeployGrants uint8

// Local grant bits are an operator-configured ceiling, not remote discovery.
const (
	LocalStage LocalDeployGrants = 1 << iota
	LocalManifest
	LocalPreview
	LocalJournalApply
	LocalNewAttended = LocalStage | LocalManifest | LocalPreview | LocalJournalApply
)

// OperatorDeployClient has no status, remove, fence, inventory, or session API.
// Its private transport also denies requests outside these exact reviewed routes.
type OperatorDeployClient struct{ monitor, deploy *Client }

func (*OperatorDeployClient) String() string               { return "[operator deploy client redacted]" }
func (*OperatorDeployClient) GoString() string             { return "[operator deploy client redacted]" }
func (*OperatorDeployClient) MarshalJSON() ([]byte, error) { return nil, ErrProtocol }

// OpenOperatorDeploy opens separate monitor and deploy transports to one
// explicitly approved origin. The monitor bearer only establishes signed
// identity. The separate deploy bearer is used only under the local allowlist;
// Dill decides current authorization per request. current must fail closed when
// the caller's approved credential or policy generation is no longer current.
func (p *Pairing) OpenOperatorDeploy(ctx context.Context, reviewed OperatorTarget, bearer string, grants LocalDeployGrants, current func(context.Context) bool) (*OperatorDeployClient, string, error) {
	if grants == 0 || grants & ^LocalNewAttended != 0 {
		return nil, "", ErrProtocol
	}
	return p.openOperatorTransports(ctx, reviewed, bearer, current, func(r *http.Request) bool { return allowedLocalDeploy(r, grants) })
}
func (p *Pairing) openOperatorTransports(ctx context.Context, reviewed OperatorTarget, bearer string, current func(context.Context) bool, allow func(*http.Request) bool) (*OperatorDeployClient, string, error) {
	if ctx == nil || ctx.Err() != nil || !p.MatchesOperatorTarget(reviewed, reviewed.Address) || allow == nil ||
		current == nil || subtle.ConstantTimeCompare([]byte(bearer), []byte(p.token)) == 1 || !current(ctx) {
		return nil, "", ErrProtocol
	}
	// Validate deploy material before contacting the monitor origin.
	validated, err := NewHTTPS(ctx, reviewed.Address, p.pin, bearer, p.binding)
	if err != nil {
		return nil, "", ErrProtocol
	}
	_ = validated.Close()
	monitor, epoch, err := p.Open(ctx, ctx, reviewed.Address)
	if err != nil {
		return nil, "", err
	}
	proof, err := monitor.httpsSession(epoch)
	if err != nil || !current(ctx) {
		_ = monitor.Close()
		return nil, "", ErrProtocol
	}
	deploy, err := NewHTTPS(proof, reviewed.Address, p.pin, bearer, p.binding)
	if err != nil {
		_ = monitor.Close()
		return nil, "", ErrProtocol
	}
	deploy.mu.Lock()
	deploy.epoch = epoch
	deploy.epochCtx, deploy.epochCancel = context.WithCancel(deploy.forward)
	deploy.mu.Unlock()
	deploy.requestGate = func(req *http.Request) bool {
		return req.URL.Opaque == "" && req.URL.Scheme == "https" && req.URL.Host == strings.TrimPrefix(deploy.baseURL, "https://") && req.Host == req.URL.Host &&
			req.URL.User == nil && req.URL.RawPath == "" && req.URL.RawQuery == "" && !req.URL.ForceQuery && req.URL.Fragment == "" &&
			allow(req) && proof.Err() == nil && req.Context().Err() == nil && current(req.Context()) && proof.Err() == nil
	}
	return &OperatorDeployClient{monitor: monitor, deploy: deploy}, epoch, nil
}

func allowedLocalDeploy(req *http.Request, local LocalDeployGrants) bool {
	var need LocalDeployGrants
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/v1/snapshots":
		need = LocalStage
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/v1/snapshots/") && canonicalHex(strings.TrimPrefix(req.URL.Path, "/v1/snapshots/"), 16):
		need = LocalManifest
	case req.Method == http.MethodPost && req.URL.Path == "/v1/deploy/preview":
		need = LocalPreview
	case req.Method == http.MethodPost && req.URL.Path == "/v1/deploy/journal-apply":
		ids := req.Header.Values(journalHeader)
		if len(ids) != 1 || !canonicalHex(ids[0], 16) {
			return false
		}
		need = LocalJournalApply
	default:
		return false
	}
	return local&need == need
}

func (c *OperatorDeployClient) Close() error { _ = c.deploy.Close(); return c.monitor.Close() }
func (c *OperatorDeployClient) Stage(ctx context.Context, epoch string, body []byte) (string, error) {
	return c.deploy.Stage(ctx, epoch, body)
}
func (c *OperatorDeployClient) Snapshot(ctx context.Context, epoch, stage string) (dillserve.Manifest, error) {
	return c.deploy.Snapshot(ctx, epoch, stage)
}
func (c *OperatorDeployClient) PreviewDeployment(ctx context.Context, epoch, stage, manifest, stack string) (*DeploymentPreview, error) {
	return c.deploy.PreviewDeployment(ctx, epoch, stage, manifest, stack)
}
func (c *OperatorDeployClient) ApplyJournalDeployment(ctx context.Context, epoch, stage, manifest, stack, selection, request, mode, journal string) (DeploymentStatus, error) {
	return c.deploy.ApplyJournalDeployment(ctx, epoch, stage, manifest, stack, selection, request, mode, journal)
}
