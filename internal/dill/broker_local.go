package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/dillserve"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type LocalBrokerConfig struct {
	Binary, Pkl, Socket, DaemonID, CoordinationDir, RuntimeDir, HistoryDir, StagingRoot, CatalogRoot string
	ProfilePath, Engine, TargetDigest, EndpointDigest                                                string
}
type LocalBroker struct {
	config  LocalBrokerConfig
	grants  map[string]bool
	current func(context.Context) bool
}
type LocalBrokerResponse struct {
	Contract  string          `json:"contract"`
	Status    int             `json:"status"`
	JournalID string          `json:"journalId,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	Code      string          `json:"code,omitempty"`
}

func NewLocalBroker(c LocalBrokerConfig, grants []string, current func(context.Context) bool) (*LocalBroker, error) {
	paths := []string{c.Binary, c.Pkl, c.CoordinationDir, c.RuntimeDir, c.HistoryDir, c.StagingRoot, c.CatalogRoot}
	if c.ProfilePath != "" {
		if c.Socket != "" || c.DaemonID != "" || (c.Engine != "docker" && c.Engine != "podman") || !operationDigestPattern.MatchString(c.TargetDigest) || !operationDigestPattern.MatchString(c.EndpointDigest) {
			return nil, brokerwire.ErrInvalid
		}
		paths = append(paths, c.ProfilePath)
	} else {
		if c.Engine != "" || c.TargetDigest != "" || c.EndpointDigest != "" {
			return nil, brokerwire.ErrInvalid
		}
		paths = append(paths, c.Socket)
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, brokerwire.ErrInvalid
		}
	}
	if c.ProfilePath == "" && (len(c.DaemonID) < 8 || len(c.DaemonID) > 128) || current == nil {
		return nil, brokerwire.ErrInvalid
	}
	known := map[string]bool{"journal": true, "stage": true, "snapshot": true, "requirements": true, "prepare": true, "retire": true, "review": true, "apply": true, "status": true, "fence": true}
	local := map[string]bool{}
	for _, name := range grants {
		if !known[name] || local[name] {
			return nil, brokerwire.ErrInvalid
		}
		local[name] = true
	}
	if len(local) == 0 {
		return nil, brokerwire.ErrInvalid
	}
	return &LocalBroker{c, local, current}, nil
}
func (*LocalBroker) MarshalJSON() ([]byte, error) { return nil, brokerwire.ErrInvalid }
func (*LocalBroker) String() string               { return "LocalBroker{redacted}" }
func (c *LocalBroker) args(action, stage, journal, id string) []string {
	q := c.config
	args := []string{"broker-local", action, "--contract", brokerwire.Version, "--coordination-state-dir", q.CoordinationDir, "--broker-store", q.RuntimeDir, "--history-dir", q.HistoryDir, "--staging-root", q.StagingRoot, "--staging-catalog", q.CatalogRoot, "--pkl", q.Pkl}
	if q.ProfilePath != "" {
		args = append(args, "--broker-profile", q.ProfilePath, "--engine-kind", q.Engine, "--target-digest", q.TargetDigest, "--endpoint-digest", q.EndpointDigest)
	} else {
		args = append(args, "--engine-socket", q.Socket, "--daemon-id", q.DaemonID)
	}
	if stage != "" {
		args = append(args, "--stage-id", stage)
	}
	if journal != "" {
		args = append(args, "--journal-id", journal)
	}
	if id != "" {
		args = append(args, "--request-id", id)
	}
	return args
}
func (c *LocalBroker) call(ctx context.Context, action, stage, journal, id string, body []byte) (LocalBrokerResponse, error) {
	var result LocalBrokerResponse
	if c == nil || ctx == nil || ctx.Err() != nil || !c.grants[action] || !c.current(ctx) {
		return result, brokerwire.ErrUnavailable
	}
	for _, v := range []string{stage, journal, id} {
		if v != "" && !brokerwire.ValidHandle(v) {
			return result, brokerwire.ErrInvalid
		}
	}
	if action != "journal" && action != "stage" && action != "snapshot" && !brokerwire.ValidHandle(journal) {
		return result, brokerwire.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	runner := Runner{Binary: c.config.Binary, PklBinary: c.config.Pkl}
	cmd, cleanup, err := runner.command(ctx, c.args(action, stage, journal, id)...)
	if err != nil {
		return result, brokerwire.ErrUnavailable
	}
	defer cleanup()
	output := operationOutput{max: brokerwire.MaxMetadata, cancel: cancel}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.Stdin = bytes.NewReader(body)
	err = runner.runTracked(ctx, cmd)
	expected := 200
	if action == "stage" {
		expected = 201
	}
	if err != nil || ctx.Err() != nil || output.overflow || !c.current(ctx) || brokerwire.DecodeMetadata(bytes.NewReader(output.buf.Bytes()), &result) != nil || result.Contract != brokerwire.Version || result.Status != expected || result.Code != "" || result.Body == nil || (journal != "" && result.JournalID != journal) {
		return LocalBrokerResponse{}, brokerwire.ErrUnavailable
	}
	return result, nil
}

type BrokerLocalJournal struct {
	Contract       string `json:"contract"`
	JournalID      string `json:"journalId"`
	TargetDigest   string `json:"targetDigest"`
	EndpointDigest string `json:"endpointDigest"`
}

func (c *LocalBroker) Journal(ctx context.Context) (BrokerLocalJournal, error) {
	var j BrokerLocalJournal
	r, err := c.call(ctx, "journal", "", "", "", nil)
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(r.Body), &j) != nil || j.Contract != brokerwire.Version || !brokerwire.ValidHandle(j.JournalID) || j.JournalID != r.JournalID || j.TargetDigest != c.expectedTarget() || j.EndpointDigest != c.expectedEndpoint() {
		return BrokerLocalJournal{}, brokerwire.ErrUnavailable
	}
	return j, nil
}
func (c *LocalBroker) Stage(ctx context.Context, body []byte) (string, error) {
	if len(body) == 0 || len(body) > 24<<20 {
		return "", brokerwire.ErrInvalid
	}
	r, err := c.call(ctx, "stage", "", "", "", body)
	if err != nil {
		return "", dillserve.ErrStageUnknown
	}
	var staged struct {
		ID     string `json:"stage_id"`
		Status string `json:"status"`
	}
	if brokerwire.DecodeMetadata(bytes.NewReader(r.Body), &staged) != nil || staged.Status != "staged" || !brokerwire.ValidHandle(staged.ID) {
		return "", brokerwire.ErrUnavailable
	}
	return staged.ID, nil
}
func (c *LocalBroker) Snapshot(ctx context.Context, stage string) (dillserve.Manifest, error) {
	if !brokerwire.ValidHandle(stage) {
		return dillserve.Manifest{}, brokerwire.ErrInvalid
	}
	r, err := c.call(ctx, "snapshot", stage, "", "", nil)
	if err != nil {
		return dillserve.Manifest{}, err
	}
	return dillserve.ParseCatalog(ctx, stage, r.Body)
}
func (c *LocalBroker) Requirements(ctx context.Context, journal, stage, manifest, stack string) (brokerwire.Requirements, error) {
	var reply struct {
		Contract       string                  `json:"contract"`
		TargetDigest   string                  `json:"targetDigest"`
		EndpointDigest string                  `json:"endpointDigest"`
		SourceDigest   string                  `json:"sourceDigest"`
		Requirements   brokerwire.Requirements `json:"requirements"`
	}
	if !brokerwire.ValidHandle(stage) || !operationDigestPattern.MatchString("sha256:"+manifest) {
		return reply.Requirements, brokerwire.ErrInvalid
	}
	body, _ := json.Marshal(map[string]string{"stack": stack, "manifestSha256": manifest})
	r, err := c.call(ctx, "requirements", stage, journal, "", body)
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(r.Body), &reply) != nil || reply.Contract != brokerwire.Version || !reply.Requirements.Valid() || reply.Requirements.Engine != c.Engine() || reply.Requirements.Stack != stack || reply.SourceDigest != "sha256:"+manifest || reply.TargetDigest != c.expectedTarget() || reply.EndpointDigest != c.expectedEndpoint() {
		return brokerwire.Requirements{}, brokerwire.ErrUnavailable
	}
	return reply.Requirements, nil
}
func (c *LocalBroker) matches(s brokerwire.Scope) bool {
	return s.Valid() && s.Engine == c.Engine() && s.TargetDigest == c.expectedTarget() && s.EndpointDigest == c.expectedEndpoint()
}
func (c *LocalBroker) Prepare(ctx context.Context, journal, stage string, e brokerwire.Envelope) (brokerwire.Review, error) {
	var reply brokerwire.PreparedReply
	if !c.matches(e.Scope) || !brokerwire.ValidHandle(stage) {
		return reply.Prepared, brokerwire.ErrInvalid
	}
	body, err := brokerwire.Encode(e)
	if err != nil {
		return reply.Prepared, err
	}
	defer clear(body)
	r, err := c.call(ctx, "prepare", stage, journal, "", body)
	if err != nil || brokerwire.DecodeMetadata(bytes.NewReader(r.Body), &reply) != nil || !reply.ValidFor(e.Scope, time.Now()) {
		return brokerwire.Review{}, brokerwire.ErrUnavailable
	}
	return reply.Prepared, nil
}
func (c *LocalBroker) Review(ctx context.Context, journal, stage string, ref brokerwire.Reference) (*BrokerReview, string, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !brokerwire.ValidHandle(stage) {
		return nil, "", brokerwire.ErrInvalid
	}
	body, _ := json.Marshal(ref)
	r, err := c.call(ctx, "review", stage, journal, "", body)
	if err != nil {
		return nil, "", err
	}
	review, err := DecodeBrokerReview(r.Body, ref)
	if err != nil || (r.Mode != "create" && r.Mode != "update") {
		return nil, "", brokerwire.ErrUnavailable
	}
	return review, r.Mode, nil
}

type BrokerLocalStatus struct {
	RequestID   string `json:"requestId"`
	Stack       string `json:"stack"`
	State       string `json:"state"`
	Effects     string `json:"effects"`
	OperationID string `json:"operationId,omitempty"`
}
type BrokerLocalOutcome struct {
	Contract  string                `json:"contract"`
	Status    BrokerLocalStatus     `json:"status"`
	Reference *brokerwire.Reference `json:"reference"`
}

var ErrBrokerLocalUnknown = errors.New("local broker outcome unknown; inspect the same request before retrying")

func decodeLocalOutcome(raw []byte, id string, ref brokerwire.Reference, allowFence bool) (BrokerLocalOutcome, error) {
	var o BrokerLocalOutcome
	if brokerwire.DecodeMetadata(bytes.NewReader(raw), &o) != nil || o.Contract != brokerwire.Version || o.Status.RequestID != id || o.Status.Stack != ref.Scope.Stack {
		return o, brokerwire.ErrUnavailable
	}
	s := o.Status
	op := s.OperationID
	if op != "" && (!strings.HasPrefix(op, "op_") || !brokerwire.ValidHandle(strings.TrimPrefix(op, "op_"))) {
		return o, brokerwire.ErrUnavailable
	}
	if allowFence && s.State == "not-admitted" && s.Effects == "none" && op == "" && o.Reference == nil {
		return o, nil
	}
	if o.Reference == nil || *o.Reference != ref {
		return o, brokerwire.ErrUnavailable
	}
	if (s.State == "unknown" || s.State == "running") && s.Effects == "unknown" && op == "" {
		return o, nil
	}
	if s.State == "succeeded" && (s.Effects == "none" || s.Effects == "complete") && op != "" {
		return o, nil
	}
	if s.State == "failed" && (s.Effects == "none" || s.Effects == "partial" && op != "") {
		return o, nil
	}
	return o, brokerwire.ErrUnavailable
}
func (c *LocalBroker) Apply(ctx context.Context, journal, stage, id, selection, mode string, ref brokerwire.Reference) (BrokerLocalOutcome, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !brokerwire.ValidHandle(stage) || !brokerwire.ValidHandle(id) || !operationDigestPattern.MatchString(selection) || (mode != "create" && mode != "update") {
		return BrokerLocalOutcome{}, brokerwire.ErrInvalid
	}
	body, _ := json.Marshal(struct {
		brokerwire.Reference
		ID        string `json:"requestId"`
		Selection string `json:"selectionDigest"`
		Confirm   string `json:"confirm"`
	}{ref, id, selection, mode})
	r, err := c.call(ctx, "apply", stage, journal, "", body)
	if err != nil {
		return BrokerLocalOutcome{}, ErrBrokerLocalUnknown
	}
	o, err := decodeLocalOutcome(r.Body, id, ref, false)
	if err != nil {
		return BrokerLocalOutcome{}, ErrBrokerLocalUnknown
	}
	return o, nil
}
func (c *LocalBroker) Status(ctx context.Context, journal, id string, ref brokerwire.Reference) (BrokerLocalOutcome, error) {
	if !ref.Valid() || !c.matches(ref.Scope) || !brokerwire.ValidHandle(id) {
		return BrokerLocalOutcome{}, brokerwire.ErrInvalid
	}
	r, err := c.call(ctx, "status", "", journal, id, nil)
	if err != nil {
		return BrokerLocalOutcome{}, err
	}
	return decodeLocalOutcome(r.Body, id, ref, true)
}
