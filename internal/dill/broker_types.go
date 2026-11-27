package dill

import (
	"bytes"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
)

type BrokerPlan struct {
	Changes []PlanChange `json:"changes"`
	Volumes []struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	} `json:"volumes,omitempty"`
	Hooks []json.RawMessage `json:"hooks,omitempty"`
}
type BrokerCLIReply struct {
	Contract  string                `json:"contract"`
	Reference *brokerwire.Reference `json:"reference,omitempty"`
	Prepared  *brokerwire.Review    `json:"prepared,omitempty"`
	Preview   json.RawMessage       `json:"preview,omitempty"`
	Plan      *BrokerPlan           `json:"plan,omitempty"`
	Operation json.RawMessage       `json:"operation,omitempty"`
	Code      string                `json:"code,omitempty"`
}
type BrokerReview struct {
	Reference brokerwire.Reference
	Preflight *PreflightResult
	Plan      *BrokerPlan
}

func DecodeBrokerReview(raw []byte, ref brokerwire.Reference) (*BrokerReview, error) {
	var reply BrokerCLIReply
	if !ref.Valid() || brokerwire.DecodeMetadata(bytes.NewReader(raw), &reply) != nil || reply.Contract != brokerwire.Version || reply.Code != "" || reply.Reference == nil || *reply.Reference != ref || reply.Plan == nil || reply.Plan.Changes == nil || len(reply.Plan.Changes) > 512 || len(reply.Plan.Hooks) != 0 {
		return nil, brokerwire.ErrUnavailable
	}
	p, err := decodePreflightResultMode(reply.Preview, true)
	if err != nil || p.Outcome != "succeeded" || p.Operation != "up" || p.Stack != ref.Scope.Stack || p.SelectionDigest == nil || p.Target == nil || p.Target.Engine != ref.Scope.Engine || p.Target.EndpointDigest != ref.Scope.EndpointDigest || p.Target.QualifiedIdentityDigest != ref.Scope.TargetDigest {
		return nil, brokerwire.ErrUnavailable
	}
	names := map[string]bool{}
	for _, ch := range reply.Plan.Changes {
		if ch.Service == "" || len(ch.Service) > 128 || names[ch.Service] || !oneOf(ch.Kind, "create", "recreate", "noop") {
			return nil, brokerwire.ErrUnavailable
		}
		names[ch.Service] = true
	}
	return &BrokerReview{ref, p, reply.Plan}, nil
}
