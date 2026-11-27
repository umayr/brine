package dill

import (
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func profileLocalConfig(t *testing.T) LocalBrokerConfig {
	root := t.TempDir()
	return LocalBrokerConfig{Binary: "/operator/dill", Pkl: "/operator/pkl", ProfilePath: "/missing/profile.json", Engine: "podman", TargetDigest: brokerwire.Digest([]byte("qualified-podman")), EndpointDigest: brokerwire.Digest([]byte("unix:///private/podman.sock")), CoordinationDir: filepath.Join(root, "coord"), RuntimeDir: filepath.Join(root, "runtime"), HistoryDir: filepath.Join(root, "history"), StagingRoot: filepath.Join(root, "stages"), CatalogRoot: filepath.Join(root, "catalog")}
}
func TestBrokerProfileLocalConfigAndActualCLIRecovery(t *testing.T) {
	cfg := profileLocalConfig(t)
	local, err := NewLocalBroker(cfg, []string{"journal", "status", "fence"}, func(context.Context) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(local.args("journal", "", "", ""), " ")
	if strings.Contains(args, "--engine-socket") || !strings.Contains(args, "--engine-kind podman") {
		t.Fatal("mixed profile selection")
	}
	for _, change := range []func(*LocalBrokerConfig){func(c *LocalBrokerConfig) { c.Socket = "/other.sock" }, func(c *LocalBrokerConfig) { c.DaemonID = "daemon-test" }, func(c *LocalBrokerConfig) { c.TargetDigest = "" }, func(c *LocalBrokerConfig) { c.Engine = "unknown" }} {
		bad := cfg
		change(&bad)
		if _, e := NewLocalBroker(bad, []string{"status"}, func(context.Context) bool { return true }); e == nil {
			t.Fatal("unscoped/mixed profile accepted")
		}
	}
	binary := os.Getenv("BRINE_TEST_DILL_BROKER_BINARY")
	if binary == "" {
		t.Skip("set native paired binary")
	}
	cfg.Binary = binary
	if err = os.Mkdir(cfg.HistoryDir, 0700); err != nil {
		t.Fatal(err)
	}
	journal := strings.Repeat("a", 32)
	wire := struct {
		Version int    `json:"version"`
		Target  string `json:"target"`
		Rows    []any  `json:"operations"`
		Journal string `json:"journalId"`
	}{2, brokerwire.Digest([]byte("dill-broker-local-profile/1\x00" + cfg.Engine + "\x00" + cfg.TargetDigest + "\x00" + cfg.EndpointDigest)), []any{}, journal}
	raw, _ := json.Marshal(wire)
	if err = os.WriteFile(filepath.Join(cfg.HistoryDir, "operations.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	local, err = NewLocalBroker(cfg, []string{"journal", "status", "fence"}, func(context.Context) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	j, err := local.Journal(context.Background())
	if err != nil || j.TargetDigest != cfg.TargetDigest {
		raw, runErr := exec.Command(binary, local.args("journal", "", "", "")...).CombinedOutput()
		t.Fatalf("paired offline profile journal: %v; native %v: %s", err, runErr, raw)
	}
	d := brokerwire.Digest([]byte("config"))
	ref := brokerwire.Reference{Handle: strings.Repeat("1", 32), Scope: brokerwire.Scope{Registration: strings.Repeat("2", 32), Generation: strings.Repeat("3", 32), Stack: "app", Engine: "podman", TargetDigest: cfg.TargetDigest, EndpointDigest: cfg.EndpointDigest, SourceDigest: d, ConfigDigest: d, PolicyRevision: d}}
	id := strings.Repeat("b", 32)
	if out, e := local.Fence(context.Background(), journal, id, ref); e != nil || out.Status.State != "not-admitted" {
		t.Fatal(e)
	}
	if out, e := local.Status(context.Background(), journal, id, ref); e != nil || out.Status.State != "not-admitted" {
		t.Fatal(e)
	}
	cfg.TargetDigest = brokerwire.Digest([]byte("retarget"))
	other, _ := NewLocalBroker(cfg, []string{"journal"}, func(context.Context) bool { return true })
	if _, err = other.Journal(context.Background()); err == nil {
		t.Fatal("retarget reused protected journal")
	}
}
func TestBrokerPodmanReviewDoesNotWidenOrdinaryDecoder(t *testing.T) {
	f := preflightFixtureRead(t, "preview-up-create.json")
	p, err := DecodePreflightResult(f.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Engine = "podman"
	raw, _ := json.Marshal(p)
	if _, err = DecodePreflightResult(raw); err == nil {
		t.Fatal("ordinary decoder admitted Podman")
	}
	d := brokerwire.Digest([]byte("scope"))
	ref := brokerwire.Reference{Handle: strings.Repeat("1", 32), Scope: brokerwire.Scope{Registration: strings.Repeat("2", 32), Generation: strings.Repeat("3", 32), Stack: p.Stack, Engine: "podman", TargetDigest: p.Target.QualifiedIdentityDigest, EndpointDigest: p.Target.EndpointDigest, SourceDigest: d, ConfigDigest: d, PolicyRevision: d}}
	reply := BrokerCLIReply{Contract: brokerwire.Version, Reference: &ref, Preview: raw, Plan: &BrokerPlan{Changes: []PlanChange{{Service: "web", Kind: "create"}}}}
	raw, _ = json.Marshal(reply)
	if _, err = DecodeBrokerReview(raw, ref); err != nil {
		t.Fatal(err)
	}
	ref.Scope.Engine = "docker"
	if _, err = DecodeBrokerReview(raw, ref); err == nil {
		t.Fatal("engine substitution accepted")
	}
}
