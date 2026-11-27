package dill

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/umayr/brine/internal/brokerwire"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerLocalActualCLIJournalFenceAndSourceStage(t *testing.T) {
	binary := os.Getenv("BRINE_TEST_DILL_BROKER_BINARY")
	if binary == "" {
		t.Skip("set native integration binary for paired local CLI check")
	}
	root := t.TempDir()
	history := filepath.Join(root, "history")
	stages := filepath.Join(root, "stages")
	catalog := filepath.Join(root, "catalog")
	for _, p := range []string{history, stages, catalog} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := LocalBrokerConfig{Binary: binary, Pkl: "/operator/pkl", Socket: "/run/docker.sock", DaemonID: "daemon-test", CoordinationDir: filepath.Join(root, "coord"), RuntimeDir: filepath.Join(root, "runtime"), HistoryDir: history, StagingRoot: stages, CatalogRoot: catalog}
	journal := strings.Repeat("a", 32)
	file := struct {
		Version int    `json:"version"`
		Target  string `json:"target"`
		Rows    []any  `json:"operations"`
		Journal string `json:"journalId"`
	}{2, brokerwire.Digest([]byte("dill-broker-local/1\x00" + cfg.Socket + "\x00" + cfg.DaemonID)), []any{}, journal}
	raw, _ := json.Marshal(file)
	if err := os.WriteFile(filepath.Join(history, "operations.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	local, err := NewLocalBroker(cfg, []string{"journal", "fence", "status", "stage", "snapshot"}, func(context.Context) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	j, err := local.Journal(context.Background())
	if err != nil || j.JournalID != journal {
		t.Fatal("actual local journal failed", err)
	}
	digest := brokerwire.Digest([]byte("config"))
	ref := brokerwire.Reference{Handle: strings.Repeat("1", 32), Scope: brokerwire.Scope{Registration: strings.Repeat("2", 32), Generation: strings.Repeat("3", 32), Stack: "app", Engine: "docker", TargetDigest: j.TargetDigest, EndpointDigest: j.EndpointDigest, SourceDigest: digest, ConfigDigest: digest, PolicyRevision: digest}}
	id := strings.Repeat("b", 32)
	out, err := local.Fence(context.Background(), journal, id, ref)
	if err != nil || out.Status.State != "not-admitted" {
		t.Fatal("actual fence failed", err)
	}
	out, err = local.Status(context.Background(), journal, id, ref)
	if err != nil || out.Status.State != "not-admitted" {
		t.Fatal("durable fence lookup failed", err)
	}
	text := []byte("synthetic source: not evaluated")
	sum := sha256.Sum256(text)
	upload := map[string]any{"schema": "dill.snapshot.v1", "entrypoint": "compose.pkl", "files": []any{map[string]any{"path": "compose.pkl", "bytes": base64.StdEncoding.EncodeToString(text), "length": len(text), "sha256": hex.EncodeToString(sum[:])}}}
	body, _ := json.Marshal(upload)
	stage, err := local.Stage(context.Background(), body)
	if err != nil {
		t.Fatal("actual stage failed", err)
	}
	manifest, err := local.Snapshot(context.Background(), stage)
	if err != nil || manifest.Entrypoint() != "compose.pkl" {
		t.Fatal("actual staged snapshot unavailable", err)
	}
}
func TestBrokerLocalGrantsAndUnknownDoNotFallBack(t *testing.T) {
	cfg := LocalBrokerConfig{Binary: "/operator/missing-dill", Pkl: "/operator/pkl", Socket: "/run/docker.sock", DaemonID: "daemon-test", CoordinationDir: "/state/coord", RuntimeDir: "/state/runtime", HistoryDir: "/state/history", StagingRoot: "/state/stages", CatalogRoot: "/state/catalog"}
	local, err := NewLocalBroker(cfg, []string{"status"}, func(context.Context) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = local.Journal(context.Background()); err == nil {
		t.Fatal("journal grant inherited")
	}
	d := brokerwire.Digest([]byte("config"))
	ref := brokerwire.Reference{Handle: strings.Repeat("1", 32), Scope: brokerwire.Scope{Registration: strings.Repeat("2", 32), Generation: strings.Repeat("3", 32), Stack: "app", Engine: "docker", TargetDigest: brokerwire.Digest([]byte("dill-local-docker-v1\x00" + cfg.DaemonID)), EndpointDigest: brokerwire.Digest([]byte("unix://" + cfg.Socket)), SourceDigest: d, ConfigDigest: d, PolicyRevision: d}}
	if _, err = local.Apply(context.Background(), strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), d, "create", ref); err != ErrBrokerLocalUnknown {
		t.Fatal("denied ambiguous dispatch inferred success", err)
	}
}
