package dill

import (
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"os"
	"strings"
	"testing"
)

func TestBrokerReviewPreservesNativePlanAndRejectsScopeSubstitution(t *testing.T) {
	raw, err := os.ReadFile("testdata/preflight-v1/preview-up-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Stdout json.RawMessage }
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture")
	}
	p, err := DecodePreflightResult(fixture.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	ref := brokerwire.Reference{Handle: strings.Repeat("1", 32), Scope: brokerwire.Scope{Registration: strings.Repeat("2", 32), Generation: strings.Repeat("3", 32), Stack: p.Stack, Engine: p.Target.Engine, TargetDigest: p.Target.QualifiedIdentityDigest, EndpointDigest: p.Target.EndpointDigest, SourceDigest: d, ConfigDigest: d, PolicyRevision: d, BindingRevision: 1}}
	reply := BrokerCLIReply{Contract: brokerwire.Version, Reference: &ref, Preview: fixture.Stdout, Plan: &BrokerPlan{Changes: []PlanChange{{Service: "web", Kind: "create", Diffs: []FieldDiff{{Field: "image", Before: "", After: "synthetic:v1"}}}}}}
	raw, err = json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	review, err := DecodeBrokerReview(raw, ref)
	if err != nil || review.Plan.Changes[0].Diffs[0].After != "synthetic:v1" {
		t.Fatal("lost native diff", err)
	}
	wrong := ref
	wrong.Scope.BindingRevision++
	if _, err = DecodeBrokerReview(raw, wrong); err == nil {
		t.Fatal("binding substitution accepted")
	}
	reply.Plan.Hooks = []json.RawMessage{json.RawMessage("{}")}
	raw, _ = json.Marshal(reply)
	if _, err = DecodeBrokerReview(raw, ref); err == nil {
		t.Fatal("unsupported hook plan accepted")
	}
}
