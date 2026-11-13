//go:build unix

package dill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in executes a source-built candidate, NEVER an engine binary or handoff
// payload. CI requires this test to pass without skips. All SDK HTTP requests
// terminate at our private Unix listener, which refuses every mutation method.
func TestOperationContractCandidateProcess(t *testing.T) {
	binary := os.Getenv("BRINE_OPERATION_CANDIDATE")
	if binary == "" {
		t.Skip("exact b3 Linux process compatibility NOT RUN: set BRINE_OPERATION_CANDIDATE via ci-operation-contract.sh")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("candidate process tests require isolated Linux, not native Mac execution")
	}
	repo := os.Getenv("BRINE_OPERATION_CANDIDATE_REPO")
	pkl := os.Getenv("BRINE_OPERATION_PKL")
	for _, p := range []string{binary, repo, pkl} {
		if !filepath.IsAbs(p) {
			t.Fatal("explicit absolute isolated candidate/Pkl/source paths required")
		}
	}
	bytesOnDisk, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytesOnDisk)
	if hex.EncodeToString(sum[:]) != os.Getenv("BRINE_OPERATION_CANDIDATE_SHA256") {
		t.Fatal("candidate binary checksum mismatch")
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["vcs.revision"] != operationCandidateCommit || settings["vcs.modified"] != "false" {
		t.Fatal("binary is not exact clean b3 source")
	}
	checkSource := func() {
		t.Helper()
		head, e := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if e != nil || strings.TrimSpace(string(head)) != operationCandidateCommit {
			t.Fatal("candidate source commit mismatch")
		}
		status, e := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=all").Output()
		if e != nil || len(bytes.TrimSpace(status)) != 0 {
			t.Fatal("candidate source is not clean")
		}
	}
	checkSource()
	t.Cleanup(checkSource)
	schema := filepath.Join(repo, "dill.pkl")
	schemaData, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	schemaSum := sha256.Sum256(schemaData)
	if hex.EncodeToString(schemaSum[:]) != operationCandidateSchemaSHA {
		t.Fatal("candidate schema mismatch")
	}
	toolchainData, err := os.ReadFile(filepath.Join(repo, "internal/loader/pkl-toolchain.json"))
	if err != nil {
		t.Fatal(err)
	}
	var toolchain struct {
		Version string            `json:"version"`
		SHA     map[string]string `json:"sha256"`
	}
	if json.Unmarshal(toolchainData, &toolchain) != nil || toolchain.Version != "0.31.1" {
		t.Fatal("Pkl source pin mismatch")
	}
	asset := "pkl-linux-amd64"
	if runtime.GOARCH == "arm64" {
		asset = "pkl-linux-aarch64"
	}
	pklData, err := os.ReadFile(pkl)
	if err != nil {
		t.Fatal(err)
	}
	pklSum := sha256.Sum256(pklData)
	if hex.EncodeToString(pklSum[:]) != toolchain.SHA[asset] {
		t.Fatal("Pkl binary differs from exact source toolchain pin")
	}
	dir, err := os.MkdirTemp("/tmp", "brine-op-sdk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	config := filepath.Join(dir, "compose.pkl")
	configData := fmt.Sprintf("amends %q\nconfig { name = \"synthetic-operation\"; engine = \"docker\"; services { [\"web\"] { image = \"synthetic.invalid/operation:v1\"; pullPolicy = \"never\" } } }\n", "file://"+schema)
	if err := os.WriteFile(config, []byte(configData), 0600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.pkl")
	if err := os.WriteFile(broken, []byte("!!!SYNTHETIC_PRIVATE_EVALUATOR!!!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(binary)
	r.PklBinary = pkl
	r.Engine = "docker"
	r.ResolveStack = func(string) (string, error) { return "app", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ids := map[string]bool{}
	run := func(args ...string) (*OperationReceipt, int, []byte) {
		t.Helper()
		cmd, cleanup, e := r.command(ctx, args...)
		if e != nil {
			t.Fatal(e)
		}
		defer cleanup()
		var out, diagnostics bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &diagnostics
		e = cmd.Run()
		if ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Fatal("candidate process did not terminate normally")
		}
		exit := cmd.ProcessState.ExitCode()
		if e != nil {
			if _, ok := e.(*exec.ExitError); !ok {
				t.Fatal("candidate failed to execute")
			}
		}
		if diagnostics.Len() != 0 {
			t.Fatal("candidate leaked diagnostics for synthetic contract invocation")
		}
		data := out.Bytes()
		if !operationLine(data, maxOperationOutput) {
			t.Fatal("not one complete newline-terminated process result")
		}
		receipt, e := parseOperationReceipt(data)
		if e != nil {
			t.Fatal("actual b3 process receipt rejected by consumer")
		}
		if receipt.OperationID == "op_00000000000000000000000000000000" || ids[receipt.OperationID] {
			t.Fatal("process operation IDs not fresh")
		}
		ids[receipt.OperationID] = true
		return receipt, exit, data
	}
	t.Run("discovery", func(t *testing.T) {
		if err := r.checkOperationCapabilities(ctx, true); err != nil {
			t.Fatalf("actual Linux b3 discovery failed: %v", err)
		}
	})
	for _, name := range []string{"unsupported-operation.json", "unsupported-version.json", "success.json", "failure.json"} {
		t.Run(name, func(t *testing.T) {
			f := operationFixture(t, name)
			args := append([]string{"operation"}, f.Args...)
			if name == "success.json" {
				args = append(args, "-f", config)
			}
			if name == "failure.json" {
				args = append(args, "-f", broken)
			}
			actual, exit, _ := run(args...)
			if exit != f.ExpectedExit {
				t.Fatalf("actual process exit %d want %d", exit, f.ExpectedExit)
			}
			expected, e := parseOperationReceipt(f.Expected)
			if e != nil {
				t.Fatal(e)
			}
			actual.OperationID = "op_00000000000000000000000000000000"
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual b3 process differs from public fixture %s", name)
			}
		})
	}
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	var forbidden []string
	prefix := regexp.MustCompile(`^/v[0-9.]+`)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := prefix.ReplaceAllString(req.URL.Path, "")
		requests = append(requests, req.Method+" "+path)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "GET" && req.Method != "HEAD" {
			forbidden = append(forbidden, req.Method+" "+path)
			http.Error(w, "writes forbidden", http.StatusMethodNotAllowed)
			return
		}
		switch path {
		case "/_ping":
			w.Header().Set("API-Version", "1.44")
			fmt.Fprint(w, "OK")
		case "/containers/app_web/json":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"synthetic not found"}`)
		case "/containers/json":
			fmt.Fprint(w, `[]`)
		default:
			forbidden = append(forbidden, req.Method+" "+path)
			http.Error(w, "unexpected read", http.StatusNotFound)
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	r.Socket = socket
	state := filepath.Join(dir, "private-state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	r.Profile = "alpha"
	r.QualifiedOperation = &QualifiedOperationConfig{StateDir: state, Layout: "native-local-docker-v1", DaemonID: "synthetic-operator-assertion-only"}
	// Exercise public Alpha routing, not merely a parser or lower-level helper.
	// This synthetic config is NOT host attestation; read-only operations must
	// omit all coordination flags and leave the private state directory empty.
	t.Run("brine-plan", func(t *testing.T) {
		result, e := r.Plan(ctx, config)
		if e != nil || result == nil || result.Operation == nil {
			t.Fatal("actual Brine plan consumer failed")
		}
		if result.Operation.Outcome != "changes" || result.Operation.Effects != "none" || !result.Operation.ChangesDetected || result.Summary.Create != 1 || result.ChangeCount() != 1 || len(result.Changes) != 0 {
			t.Fatal("consumer lost read-only opaque plan semantics")
		}
		if ids[result.Operation.OperationID] {
			t.Fatal("reused operation ID")
		}
		ids[result.Operation.OperationID] = true
	})
	t.Run("brine-validate", func(t *testing.T) {
		if e := r.Validate(ctx, config); e != nil {
			t.Fatal("actual public Alpha Brine offline validate consumer failed")
		}
		if e := r.ValidateContent(ctx, config, configData); e == nil {
			t.Fatal("unsupported stdin silently fell back")
		}
	})
	for _, op := range []string{"plan", "diff"} {
		t.Run("cli-"+op, func(t *testing.T) {
			receipt, exit, _ := run("operation", op, "--contract", "dill.operation/1", "--format", "json", "--engine", "docker", "--engine-socket", socket, "-p", "app", "-f", config)
			want := 0
			if op == "plan" {
				want = 1
			}
			if exit != want || receipt.Outcome != "changes" || receipt.Effects != "none" || !receipt.ChangesDetected || len(receipt.Resources) != 1 || receipt.Resources[0].Action != "create" {
				t.Fatal("read-only actual process semantics mismatch")
			}
		})
	}
	stateEntries, err := os.ReadDir(state)
	if err != nil || len(stateEntries) != 0 {
		t.Fatal("read-only operations unexpectedly initialized coordination state")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forbidden) != 0 {
		t.Fatalf("unexpected SDK calls (including attempted writes): %v", forbidden)
	}
	sawInspect, sawList := false, false
	for _, req := range requests {
		if strings.HasSuffix(req, " /containers/app_web/json") {
			sawInspect = true
		}
		if strings.HasSuffix(req, " /containers/json") {
			sawList = true
		}
	}
	if !sawInspect || !sawList {
		t.Fatal("positive plan never reached real Docker SDK synthetic HTTP reads")
	}
	t.Log("actual clean-b3 CLI and Brine consumer discovery/validate/plan/diff passed over synthetic Unix HTTP; no real engine, mutation, native qualification, app/browser acceptance")
}
