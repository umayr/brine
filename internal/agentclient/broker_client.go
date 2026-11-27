package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/dillserve"
	"io"
	"net/http"
	"strings"
	"time"
)

type BrokerGrants uint16

const (
	BrokerStage BrokerGrants = 1 << iota
	BrokerManifest
	BrokerRequirements
	BrokerPrepare
	BrokerReview
	BrokerApply
	BrokerStatus
	BrokerFence
	BrokerRecoveryStatus
	BrokerRetire
	brokerAll = BrokerStage | BrokerManifest | BrokerRequirements | BrokerPrepare | BrokerReview | BrokerApply | BrokerStatus | BrokerFence | BrokerRecoveryStatus | BrokerRetire
)

type BrokerClient struct {
	pair         *OperatorDeployClient
	targetDigest string
}

func (*BrokerClient) MarshalJSON() ([]byte, error) { return nil, ErrProtocol }
func (*BrokerClient) String() string               { return "BrokerClient{redacted}" }
func (*BrokerClient) GoString() string             { return "BrokerClient{redacted}" }
func (p *Pairing) OpenBrokerDeploy(ctx context.Context, reviewed OperatorTarget, bearer string, grants BrokerGrants, current func(context.Context) bool, brokerTargets ...string) (*BrokerClient, string, error) {
	if len(brokerTargets) > 1 || reviewed.EngineKind != "docker" {
		return nil, "", ErrProtocol
	}
	target := ""
	if len(brokerTargets) == 1 {
		target = brokerTargets[0]
		if !(strings.HasPrefix(target, "sha256:") && len(target) == 71 && canonicalHex(target[7:], 32)) {
			return nil, "", ErrProtocol
		}
	}
	if grants == 0 || grants & ^brokerAll != 0 {
		return nil, "", ErrProtocol
	}
	pair, epoch, err := p.openOperatorTransports(ctx, reviewed, bearer, current, func(r *http.Request) bool { return allowedBrokerDeploy(r, grants) })
	if err != nil {
		return nil, "", err
	}
	return &BrokerClient{pair: pair, targetDigest: target}, epoch, nil
}
func allowedBrokerDeploy(r *http.Request, g BrokerGrants) bool {
	var need BrokerGrants
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/snapshots":
		need = BrokerStage
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/snapshots/") && canonicalHex(strings.TrimPrefix(r.URL.Path, "/v1/snapshots/"), 16):
		need = BrokerManifest
	case r.Method == "GET" && r.URL.Path == "/v1/broker-secrets/journal":
		need = BrokerStatus
	case r.Method == "POST" && r.URL.Path == "/v1/deploy/fence":
		need = BrokerFence
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/deploy/operations/") && canonicalHex(strings.TrimPrefix(r.URL.Path, "/v1/deploy/operations/"), 16):
		need = BrokerRecoveryStatus
	default:
		for _, v := range []struct {
			method, part string
			grant        BrokerGrants
		}{{"POST", "requirements", BrokerRequirements}, {"POST", "prepare", BrokerPrepare}, {"POST", "retire", BrokerRetire}, {"POST", "review", BrokerReview}, {"POST", "apply", BrokerApply}, {"GET", "operations", BrokerStatus}} {
			if id, ok := strings.CutPrefix(r.URL.Path, "/v1/broker-secrets/"+v.part+"/"); ok && r.Method == v.method && canonicalHex(id, 16) {
				need = v.grant
				break
			}
		}
	}
	return need != 0 && g&need == need
}
func (c *BrokerClient) Close() error { return c.pair.Close() }
func (c *BrokerClient) Stage(ctx context.Context, epoch string, body []byte) (string, error) {
	return c.pair.Stage(ctx, epoch, body)
}
func (c *BrokerClient) Snapshot(ctx context.Context, epoch, stage string) (dillserve.Manifest, error) {
	return c.pair.Snapshot(ctx, epoch, stage)
}
func (c *BrokerClient) Fence(ctx context.Context, epoch, id, stack, journal string) (DeploymentStatus, error) {
	return c.pair.deploy.FenceDeployment(ctx, epoch, id, stack, journal)
}
func (c *BrokerClient) RecoveryStatus(ctx context.Context, epoch, id, stack, journal string) (DeploymentStatus, error) {
	return c.pair.deploy.JournalDeploymentStatus(ctx, epoch, id, stack, journal)
}

type BrokerJournal struct {
	Contract       string `json:"contract"`
	JournalID      string `json:"journalId"`
	TargetDigest   string `json:"targetDigest"`
	EndpointDigest string `json:"endpointDigest"`
}

func (c *BrokerClient) call(ctx context.Context, epoch, method, path, journal string, body []byte, limit time.Duration) ([]byte, http.Header, error) {
	if path != "/v1/broker-secrets/journal" && !canonicalHex(journal, 16) {
		return nil, nil, ErrProtocol
	}
	session, err := c.pair.deploy.httpsSession(epoch)
	if ctx == nil || err != nil {
		return nil, nil, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	stop := context.AfterFunc(session, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, method, c.pair.deploy.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, ErrProtocol
	}
	req.GetBody = nil
	req.Header.Set("X-Dill-Broker-Contract", brokerwire.Version)
	req.Header.Set("Cache-Control", "no-store")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if journal != "" {
		if !canonicalHex(journal, 16) {
			return nil, nil, ErrProtocol
		}
		req.Header.Set(journalHeader, journal)
	}
	resp, err := c.pair.deploy.do(req)
	if err != nil {
		return nil, nil, ErrProtocol
	}
	defer resp.Body.Close()
	ids := resp.Header.Values(journalHeader)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || len(ids) != 1 || !canonicalHex(ids[0], 16) || (journal != "" && ids[0] != journal) {
		return nil, nil, ErrProtocol
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, brokerwire.MaxMetadata+1))
	if err != nil || len(raw) > brokerwire.MaxMetadata || ctx.Err() != nil || session.Err() != nil {
		return nil, nil, ErrProtocol
	}
	return raw, resp.Header, nil
}
func (c *BrokerClient) Journal(ctx context.Context, epoch string) (BrokerJournal, error) {
	var j BrokerJournal
	raw, headers, err := c.call(ctx, epoch, "GET", "/v1/broker-secrets/journal", "", nil, 15*time.Second)
	b := c.pair.deploy.binding
	if err != nil || decodeStrict(raw, &j) != nil || j.Contract != brokerwire.Version || j.JournalID != headers.Get(journalHeader) || j.TargetDigest != c.expectedBrokerTarget() || j.EndpointDigest != brokerwire.Digest([]byte(b.EngineSocket)) {
		return BrokerJournal{}, ErrProtocol
	}
	return j, nil
}
func (c *BrokerClient) Requirements(ctx context.Context, epoch, journal, stage, manifest, stack string) (brokerwire.Requirements, error) {
	var reply struct {
		Contract       string                  `json:"contract"`
		TargetDigest   string                  `json:"targetDigest"`
		EndpointDigest string                  `json:"endpointDigest"`
		SourceDigest   string                  `json:"sourceDigest"`
		Requirements   brokerwire.Requirements `json:"requirements"`
	}
	if !canonicalHex(stage, 16) || !canonicalHex(manifest, 32) {
		return reply.Requirements, ErrProtocol
	}
	body, _ := json.Marshal(map[string]string{"stack": stack, "manifestSha256": manifest})
	raw, _, err := c.call(ctx, epoch, "POST", "/v1/broker-secrets/requirements/"+stage, journal, body, 35*time.Second)
	b := c.pair.deploy.binding
	if err != nil || decodeStrict(raw, &reply) != nil || reply.Contract != brokerwire.Version || !reply.Requirements.Valid() || reply.Requirements.Engine != c.pair.deploy.binding.EngineKind || reply.Requirements.Stack != stack || reply.SourceDigest != "sha256:"+manifest || reply.TargetDigest != c.expectedBrokerTarget() || reply.EndpointDigest != brokerwire.Digest([]byte(b.EngineSocket)) {
		return brokerwire.Requirements{}, ErrProtocol
	}
	return reply.Requirements, nil
}
func (c *BrokerClient) matches(s brokerwire.Scope) bool {
	b := c.pair.deploy.binding
	return s.Valid() && s.Engine == b.EngineKind && s.TargetDigest == c.expectedBrokerTarget() && s.EndpointDigest == brokerwire.Digest([]byte(b.EngineSocket))
}
func (c *BrokerClient) Prepare(ctx context.Context, epoch, journal, stage string, e brokerwire.Envelope) (brokerwire.Review, error) {
	var reply struct {
		Contract string            `json:"contract"`
		Prepared brokerwire.Review `json:"prepared"`
	}
	if !canonicalHex(stage, 16) || !c.matches(e.Scope) {
		return reply.Prepared, ErrProtocol
	}
	body, err := brokerwire.Encode(e)
	if err != nil {
		return reply.Prepared, ErrProtocol
	}
	defer clear(body)
	raw, _, err := c.call(ctx, epoch, "POST", "/v1/broker-secrets/prepare/"+stage, journal, body, 35*time.Second)
	r := &reply.Prepared
	if err != nil || decodeStrict(raw, &reply) != nil || reply.Contract != brokerwire.Version || r.Scope != e.Scope || r.Active || !brokerwire.ValidHandle(r.Handle) || !r.ExpiresAt.After(time.Now()) || r.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
		return brokerwire.Review{}, ErrProtocol
	}
	return *r, nil
}
func (c *BrokerClient) Review(ctx context.Context, epoch, journal, stage string, ref brokerwire.Reference) (*dill.BrokerReview, string, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !canonicalHex(stage, 16) {
		return nil, "", ErrProtocol
	}
	body, _ := json.Marshal(ref)
	raw, headers, err := c.call(ctx, epoch, "POST", "/v1/broker-secrets/review/"+stage, journal, body, 35*time.Second)
	if err != nil {
		return nil, "", ErrProtocol
	}
	r, err := dill.DecodeBrokerReview(raw, ref)
	mode := headers.Get("X-Dill-Deployment-Mode")
	if err != nil || (mode != "create" && mode != "update") {
		return nil, "", ErrProtocol
	}
	return r, mode, nil
}

type BrokerStatusResult struct {
	Contract  string                `json:"contract"`
	Status    DeploymentStatus      `json:"status"`
	Reference *brokerwire.Reference `json:"reference"`
}

func (c *BrokerClient) Apply(ctx context.Context, epoch, journal, stage, id, selection, mode string, ref brokerwire.Reference) (BrokerStatusResult, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !canonicalHex(stage, 16) || !canonicalHex(id, 16) || !strings.HasPrefix(selection, "sha256:") || !canonicalHex(strings.TrimPrefix(selection, "sha256:"), 32) || (mode != "create" && mode != "update") {
		return BrokerStatusResult{}, ErrProtocol
	}
	body, _ := json.Marshal(struct {
		brokerwire.Reference
		RequestID string `json:"requestId"`
		Selection string `json:"selectionDigest"`
		Confirm   string `json:"confirm"`
	}{ref, id, selection, mode})
	raw, _, err := c.call(ctx, epoch, "POST", "/v1/broker-secrets/apply/"+stage, journal, body, 6*time.Minute)
	result, e := decodeBrokerStatus(raw, id, ref)
	if err != nil || e != nil || result.Status.State == "not-admitted" {
		return BrokerStatusResult{}, ErrDeploymentUnknown
	}
	return result, nil
}
func (c *BrokerClient) Status(ctx context.Context, epoch, journal, id string, ref brokerwire.Reference) (BrokerStatusResult, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !canonicalHex(id, 16) {
		return BrokerStatusResult{}, ErrProtocol
	}
	raw, _, err := c.call(ctx, epoch, "GET", "/v1/broker-secrets/operations/"+id, journal, nil, 15*time.Second)
	if err != nil {
		return BrokerStatusResult{}, ErrProtocol
	}
	return decodeBrokerStatus(raw, id, ref)
}
func decodeBrokerStatus(raw []byte, id string, ref brokerwire.Reference) (BrokerStatusResult, error) {
	var r BrokerStatusResult
	if decodeStrict(raw, &r) != nil || r.Contract != brokerwire.Version || r.Status.RequestID != id || r.Status.Stack != ref.Scope.Stack {
		return r, ErrProtocol
	}
	s := r.Status
	op := s.OperationID
	if op != "" && (!strings.HasPrefix(op, "op_") || !canonicalHex(strings.TrimPrefix(op, "op_"), 16)) {
		return r, ErrProtocol
	}
	if s.State == "not-admitted" && s.Effects == "none" && op == "" && r.Reference == nil {
		return r, nil
	}
	if r.Reference == nil || *r.Reference != ref {
		return r, ErrProtocol
	}
	if (s.State == "running" || s.State == "unknown") && s.Effects == "unknown" && op == "" {
		return r, nil
	}
	if s.State == "succeeded" && (s.Effects == "complete" || s.Effects == "none") && op != "" {
		return r, nil
	}
	if s.State == "failed" && (s.Effects == "none" || s.Effects == "partial" && op != "") {
		return r, nil
	}
	return r, ErrProtocol
}
