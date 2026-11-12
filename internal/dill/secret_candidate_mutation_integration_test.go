//go:build unix

package dill

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/umayr/brine/internal/secretbindings"
	"golang.org/x/sys/unix"
)

// This test MUTATES an explicitly disposable, pre-provisioned Linux engine.
// It creates its own private synthetic sources and never uses caller source paths.
func TestCandidateDillSecretDisposableMutation(t *testing.T) {
	if os.Getenv("BRINE_DILL_DISPOSABLE_MUTATION") != "I_HAVE_A_DISPOSABLE_LINUX_ENGINE" {
		t.Skip("disposable Linux engine mutation not explicitly enabled")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("disposable mutation requires Linux")
	}
	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("%s required for disposable test", key)
		}
		return v
	}
	binary := required("BRINE_DILL_CANDIDATE")
	checksum := required("BRINE_DILL_CANDIDATE_SHA256")
	commit := required("BRINE_DILL_CANDIDATE_COMMIT")
	repo := required("BRINE_DILL_CANDIDATE_REPO")
	schema := required("BRINE_DILL_SCHEMA")
	pkl := required("BRINE_DILL_PKL")
	engine := required("BRINE_DILL_ENGINE")
	socket := required("BRINE_DILL_SOCKET")
	image := required("BRINE_DILL_TEST_IMAGE")
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(checksum) || !filepath.IsAbs(binary) || !filepath.IsAbs(repo) || !filepath.IsAbs(schema) || !filepath.IsAbs(pkl) || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || (engine != "docker" && engine != "podman") {
		t.Fatal("invalid candidate identity or local engine configuration")
	}
	actual, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(actual)
	if hex.EncodeToString(sum[:]) != checksum {
		t.Fatal("candidate checksum mismatch")
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != commit {
		t.Fatal("candidate commit mismatch")
	}
	dirty, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil || len(dirty) != 0 {
		t.Fatal("candidate checkout is not clean")
	}
	a, e := os.Stat(schema)
	b, e2 := os.Stat(filepath.Join(repo, "dill.pkl"))
	if e != nil || e2 != nil || !os.SameFile(a, b) {
		t.Fatal("schema must be the pinned candidate schema")
	}
	grantUID, err := strconv.Atoi(required("BRINE_DILL_TEST_GRANT_UID"))
	if err != nil || grantUID < 0 {
		t.Fatal("invalid grant uid")
	}
	grantGID, err := strconv.Atoi(required("BRINE_DILL_TEST_GRANT_GID"))
	if err != nil || grantGID < 0 {
		t.Fatal("invalid grant gid")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".brine-disposable-ci-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		t.Fatal("fixture root must be canonical")
	}
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := hex.EncodeToString(nonce[:])
	stack := "brine-ci-" + marker
	write := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, ".brine-fixture-owner"), []byte(marker), 0600)
	workspace := filepath.Join(root, "workspace")
	sourceDir := filepath.Join(root, "sources")
	for _, dir := range []string{workspace, sourceDir} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(sourceDir, "synthetic")
	write(source, []byte("synthetic-ci-only\n"), 0400)
	source2 := filepath.Join(sourceDir, "synthetic-r2")
	write(source2, []byte("synthetic-ci-rotation\n"), 0400)
	config := filepath.Join(workspace, "compose.pkl")
	importPath := filepath.Join(workspace, "settings.pkl")
	policyPath := filepath.Join(root, "policy.json")
	settings := []byte("label = \"one\"\n")
	write(importPath, settings, 0600)
	configBytes := []byte(fmt.Sprintf(`amends %q
import "settings.pkl"
config {
 name = %q
 engine = %q
 secrets { new { name = "db" } }
 services { ["app"] {
  image = %q
  pullPolicy = "never"
  labels { ["fixture-import"] = settings.label; ["fixture-config"] = "one" }
  secrets { new { name = "db"; target = "/run/secrets/db"; uid = %d; gid = %d; mode = 256 } }
 } }
}
`, "file://"+schema, stack, engine, image, grantUID, grantGID))
	write(config, configBytes, 0600)
	type policySource struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
		Kind     string `json:"kind"`
		Path     string `json:"path"`
		UID      int    `json:"uid"`
		GID      int    `json:"gid"`
		Mode     int    `json:"mode"`
	}
	src := policySource{"db-source", "r1", "hostFile", source, os.Getuid(), os.Getgid(), 256}
	makePolicy := func(sources []policySource) []byte {
		data, e := json.Marshal(map[string]any{"version": "dill.secret-policy/1", "targets": []any{map[string]any{"id": "lab-a", "engine": engine, "socket": socket, "stacks": []any{map[string]any{"name": stack, "sources": sources}}}}})
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	src2 := src
	src2.Revision = "r2"
	src2.Path = source2
	policy := makePolicy([]policySource{src, src2})
	write(policyPath, policy, 0600)
	private := filepath.Join(root, "state")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := secretbindings.Open(private, workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	coordinator := &secretbindings.Coordinator{}
	runner := New(binary)
	runner.PklBinary = pkl
	runner.Engine = engine
	runner.Socket = socket
	runner.ResolveStack = func(path string) (string, error) {
		if path != config {
			return "", fmt.Errorf("unexpected workspace")
		}
		return stack, nil
	}
	s := &SecretRunner{Runner: runner, State: state, Coordinator: coordinator, Target: "lab-a", Engine: engine, Socket: socket, Policy: policyPath}
	id := secretbindings.DeploymentID(stack)
	binding := []byte(fmt.Sprintf(`{"version":"dill.secret-bindings/1","target":"lab-a","stack":%q,"bindings":[{"name":"db","source":"db-source","revision":"r1"}]}`, stack))
	if err = state.SaveDraft(id, stack, "lab-a", binding); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	defer func() {
		var out bytes.Buffer
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if e := runner.Down(cleanupCtx, config, &out); e != nil {
			t.Errorf("disposable stack cleanup failed: %v", e)
		}
	}()
	cli, err := client.NewClientWithOpts(client.WithHost("unix://"+socket), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	assertMount := func(expected string) {
		t.Helper()
		info, e := cli.ContainerInspect(ctx, stack+"_app")
		if e != nil {
			t.Fatalf("inspect fixture: %v", e)
		}
		if !info.State.Running || info.Config.Labels["dill.stack"] != stack || info.Config.Labels["dill.secret-enabled"] != "true" {
			t.Fatal("fixture ownership or running state invalid")
		}
		found := false
		for _, mount := range info.Mounts {
			if mount.Destination == "/run/secrets/db" {
				found = mount.Source == expected && !mount.RW
			}
		}
		if !found {
			t.Fatal("expected read-only synthetic host mount not found")
		}
	}
	preview := func() string {
		t.Helper()
		if _, e := s.Preview(ctx, id, stack, config); e != nil {
			t.Fatalf("preview failed: %v", e)
		}
		p, e := state.Preview(id, stack, "lab-a")
		if e != nil {
			t.Fatal(e)
		}
		return p.Token
	}
	if err = s.ConfirmAndApply(ctx, id, stack, config, preview()); err != nil {
		t.Fatalf("initial checked deployment failed: %v", err)
	}
	assertMount(source)
	// The paired Dill label-only fast-path must not discover or execute Pkl.
	evalMarker := filepath.Join(root, "unexpected-pkl-execution")
	unavailablePkl := filepath.Join(root, "unavailable-pkl")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	write(unavailablePkl, []byte("#!/bin/sh\ntouch "+quote(evalMarker)+"\nexit 1\n"), 0700)
	assertNoPkl := func() {
		t.Helper()
		if _, err := os.Stat(evalMarker); !os.IsNotExist(err) {
			t.Fatal("label cleanup invoked Pkl")
		}
	}
	checkStale := func(name string, mutate, restore func()) {
		t.Helper()
		token := preview()
		mutate()
		before, beforeErr := cli.ContainerInspect(ctx, stack+"_app")
		e := s.ConfirmAndApply(ctx, id, stack, config, token)
		after, afterErr := cli.ContainerInspect(ctx, stack+"_app")
		restore()
		if !errors.Is(e, secretbindings.ErrStale) {
			t.Fatalf("%s change did not reject stale preview before apply: %v", name, e)
		}
		if beforeErr != nil || afterErr != nil || before.State == nil || after.State == nil || before.ID != after.ID || before.State.Running != after.State.Running || before.State.Status != after.State.Status || before.State.StartedAt != after.State.StartedAt || before.State.FinishedAt != after.State.FinishedAt || before.RestartCount != after.RestartCount {
			t.Fatalf("%s confirmation changed live state", name)
		}
		active, e := state.Active(id, stack, "lab-a")
		if e != nil || !bytes.Equal(active, binding) {
			t.Fatalf("%s changed active binding", name)
		}
	}
	checkStale("config", func() {
		write(config, bytes.Replace(configBytes, []byte(`"fixture-config"] = "one"`), []byte(`"fixture-config"] = "two"`), 1), 0600)
	}, func() { write(config, configBytes, 0600) })
	checkStale("import", func() { write(importPath, []byte("label = \"two\"\n"), 0600) }, func() { write(importPath, settings, 0600) })
	second := src
	second.ID = "other-source"
	checkStale("policy", func() { write(policyPath, makePolicy([]policySource{src, src2, second}), 0600) }, func() { write(policyPath, policy, 0600) })
	checkStale("live engine", func() {
		var out bytes.Buffer
		runner.PklBinary = unavailablePkl
		e := runner.Stop(ctx, config, &out)
		runner.PklBinary = pkl
		if e != nil {
			t.Fatalf("stop fixture without Pkl: %v", e)
		}
		assertNoPkl()
		info, e := cli.ContainerInspect(ctx, stack+"_app")
		if e != nil || info.State == nil || info.State.Running {
			t.Fatal("label stop did not stop fixture")
		}
	}, func() {
		if e := s.ApplyActive(ctx, id, stack, config); e != nil {
			t.Fatalf("restore fixture: %v", e)
		}
	})
	binding2 := bytes.Replace(binding, []byte(`"revision":"r1"`), []byte(`"revision":"r2"`), 1)
	if err = state.SaveDraft(id, stack, "lab-a", binding2); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmAndApply(ctx, id, stack, config, preview()); err != nil {
		t.Fatalf("checked rotation failed: %v", err)
	}
	if err = s.CheckActive(ctx, id, stack, config); err != nil {
		t.Fatalf("rotated source unavailable: %v", err)
	}
	assertMount(source2)
	// Deliberate external disappearance injection on this test-owned source directory.
	markerPath := filepath.Join(root, ".brine-fixture-owner")
	markerData, err := os.ReadFile(markerPath)
	resolved, resolveErr := filepath.EvalSymlinks(sourceDir)
	for _, path := range []string{root, sourceDir, source, source2, markerPath} {
		var stat unix.Stat_t
		if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&unix.S_IFMT == unix.S_IFLNK {
			t.Fatal("refusing non-owned or symlinked fixture")
		}
	}
	if err != nil || string(markerData) != marker || resolveErr != nil || resolved != sourceDir || filepath.Dir(sourceDir) != root {
		t.Fatal("refusing to alter non-owned fixture")
	}
	missing := sourceDir + ".unavailable"
	if err = os.Rename(sourceDir, missing); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := os.Stat(missing); e == nil {
			if e = os.Rename(missing, sourceDir); e != nil {
				t.Errorf("restore synthetic source: %v", e)
			}
		}
	}()
	if err = s.CheckActive(ctx, id, stack, config); err == nil {
		t.Fatal("missing source reported available")
	}
	var output bytes.Buffer
	runner.PklBinary = unavailablePkl
	err = runner.Down(ctx, config, &output)
	runner.PklBinary = pkl
	if err != nil {
		t.Fatalf("cleanup with missing source and unavailable Pkl failed: %v", err)
	}
	assertNoPkl()
	if _, err = cli.ContainerInspect(ctx, stack+"_app"); !errdefs.IsNotFound(err) {
		t.Fatal("fixture container remains after cleanup")
	}
	for _, item := range []struct{ name, content string }{{"synthetic", "synthetic-ci-only\n"}, {"synthetic-r2", "synthetic-ci-rotation\n"}} {
		path := filepath.Join(missing, item.name)
		data, e := os.ReadFile(path)
		info, e2 := os.Lstat(path)
		if e != nil || e2 != nil || string(data) != item.content || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
			t.Fatal("product cleanup modified synthetic source")
		}
	}
	if err = os.Rename(missing, sourceDir); err != nil {
		t.Fatal(err)
	}
}
