package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"github.com/umayr/brine/internal/secretbindings"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/store"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A second real CLI process exercises Source History's supported local-only
// startup path, not an unmounted attended adapter or test-only auth authority.
func setupSourceRestoreProcess(t *testing.T, ctx context.Context, stack, socket, dillBin string, compose, schema []byte, phase *atomic.Int32, seen *atomic.Bool, deadline *atomic.Int64, engineCall func(string, string) []byte) (http.Handler, func()) {
	t.Helper()
	root, e := os.MkdirTemp("/source-home", "restore-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	data, archive, keys := filepath.Join(root, "data"), filepath.Join(root, "archive"), filepath.Join(root, "keys")
	for _, d := range []string{data, archive, keys} {
		if e = os.Mkdir(d, 0700); e != nil {
			t.Fatal(e)
		}
	}
	writeKey := func(name string) string {
		b := make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			t.Fatal(e)
		}
		p := filepath.Join(keys, name)
		if e = os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		return p
	}
	sourceKey, hookKey := writeKey("source.key"), writeKey("hooks.key")
	// Explicitly provision the empty private protection-state root; startup must
	// never create missing protection state as permission to mutate source.
	if e = os.Mkdir(filepath.Join(data, "secret-state"), 0700); e != nil {
		t.Fatal(e)
	}
	fs, e := store.NewFSStore(data)
	if e != nil {
		t.Fatal(e)
	}
	name := stack + "-restore"
	initial := []byte("// retained synthetic source\n" + string(compose))
	st, e := fs.Create(name, string(initial))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(st.PklPath), "dill.pkl"), schema, 0600); e != nil {
		t.Fatal(e)
	}
	// Seed monotonic protection metadata offline with its production writer.
	// This is a protected-state fixture, not a claimed secret-deployment flow.
	protectedName := name + "-protected"
	protected, e := fs.Create(protectedName, string(initial))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(filepath.Dir(protected.PklPath), "dill.pkl"), schema, 0600); e != nil {
		t.Fatal(e)
	}
	state, e := secretbindings.Open(filepath.Join(data, "secret-state"), filepath.Join(data, "stacks"))
	if e != nil {
		t.Fatal(e)
	}
	if e = state.MarkSecretEnabled(secretbindings.DeploymentID(protectedName)); e != nil {
		state.Close()
		t.Fatal(e)
	}
	if e = state.Close(); e != nil {
		t.Fatal(e)
	}
	binary := filepath.Join(root, "brine")
	wd, _ := os.Getwd()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/brine")
	build.Dir = filepath.Clean(filepath.Join(wd, "../.."))
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build production Brine: %v %s", e, out)
	}
	base := []string{"--data-dir", data}
	sourceArgs := []string{"--source-history-dir", archive, "--source-history-key", sourceKey}
	hookArgs := []string{"--webhooks-dir", filepath.Join(data, "webhooks"), "--webhooks-key-file", hookKey}
	command := func(args ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, binary, args...)
		c.Env = append(os.Environ(), "BRINE_AUTH_USER=admin", "BRINE_AUTH_PASSWORD=fixture-only-password")
		return c
	}
	for _, args := range [][]string{append(append(append([]string{}, base...), sourceArgs...), "--init-source-history"), append(append(append([]string{}, base...), hookArgs...), "--webhooks-enabled", "--init-webhooks")} {
		if out, e := command(args...).CombinedOutput(); e != nil {
			t.Fatalf("offline source/hook initialization: %v %s", e, out)
		}
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	listener.Close()
	args := append(append(append([]string{}, base...), sourceArgs...), hookArgs...)
	args = append(args, "--webhooks-enabled", "--bind", "127.0.0.1", "--port", port, "--engine", "docker", "--engine-socket", "unix://"+socket, "--dill", dillBin)
	process := command(args...)
	var logs bytes.Buffer
	process.Stderr = &logs
	isolateFixtureProcess(process)
	if e = process.Start(); e != nil {
		t.Fatal(e)
	}
	var stopOnce sync.Once
	stopSource := func() { stopOnce.Do(func() { killFixtureGroup(process); process.Wait() }) }
	t.Cleanup(stopSource)
	client := &http.Client{Timeout: 2 * time.Second}
	ready := false
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
		r, e := client.Get("http://" + addr + "/login")
		if e == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		stopSource()
		t.Fatal("production source process did not start", logs.String())
	}
	upstream, _ := url.Parse("http://" + addr)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	run := func() {
		wait := func(n int) store.SourceView {
			end := time.Now().Add(40 * time.Second)
			for time.Now().Before(end) {
				v, e := fs.ReviewSource(ctx, name)
				if e == nil && len(v.Revisions) == n {
					return v
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("browser source revision count not observed", n)
			return store.SourceView{}
		}
		runtimeProof := func() []byte {
			var rows []struct {
				ID     string `json:"Id"`
				Labels map[string]string
			}
			if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &rows) != nil {
				t.Fatal("runtime proof unavailable")
			}
			proof := map[string]any{}
			for _, r := range rows {
				// Observe every disposable engine container, including an unintended
				// new deployment under the Restore source's different stack name.
				var item struct {
					ID    string `json:"Id"`
					State struct {
						Running   bool
						StartedAt string
					}
					Config struct{ Cmd []string }
					Mounts []struct{ Name, Source, Destination string }
				}
				if json.Unmarshal(engineCall("GET", "/containers/"+r.ID+"/json"), &item) != nil || item.ID != r.ID {
					t.Fatal("runtime inspection unavailable")
				}
				sort.Slice(item.Mounts, func(i, j int) bool { return item.Mounts[i].Destination < item.Mounts[j].Destination })
				proof[r.ID] = item
			}
			if len(proof) < 2 {
				t.Fatal("runtime proof membership differs", len(proof))
			}
			b, _ := json.Marshal(proof)
			return b
		}
		baseline := runtimeProof()
		before := wait(1)
		changed := append(append([]byte{}, initial...), []byte("\n// current source to preserve before restore\n")...)
		if _, e := fs.Update(name, string(changed)); e != nil {
			t.Fatal(e)
		}
		phase.Store(3)
		after := wait(3)
		source, e := fs.ReadPkl(name)
		if e != nil || source != string(initial) {
			t.Fatal("Restore did not recover exact source")
		}
		verifyArchive := func() {
			vault, e := snapshothistory.OpenSourceArchive(archive, sourceKey, data, snapshothistory.SourceArchiveLimits{})
			if e != nil {
				t.Fatal(e)
			}
			defer vault.Close()
			if after.Revisions[0] != before.Revisions[0] {
				t.Fatal("Restore replaced lineage")
			}
			for i, id := range after.Revisions {
				record, e := vault.Get(after.Scope, id)
				if e != nil {
					t.Fatal(e)
				}
				want := initial
				if i == 1 {
					want = changed
				}
				if !reflect.DeepEqual(record.Files, map[string][]byte{"compose.pkl": want, "dill.pkl": schema}) {
					t.Fatal("retained/restored source bytes differ", i)
				}
				if i > 0 && record.Parent != after.Revisions[i-1] {
					t.Fatal("revision parent differs")
				}
				if i == 2 && record.RestoredFrom != after.Revisions[0] {
					t.Fatal("restore provenance absent")
				}
			}
		}
		// No deployment is invoked by this helper or by source retention/restore.
		var inventory []struct {
			ID     string `json:"Id"`
			Labels map[string]string
		}
		if json.Unmarshal(engineCall("GET", "/containers/json?all=1"), &inventory) != nil {
			t.Fatal("runtime inventory unavailable after source restore")
		}
		if !bytes.Equal(baseline, runtimeProof()) {
			t.Fatal("source Restore changed runtime identity/state/command/mounts")
		}
		end := time.Now().Add(45 * time.Second)
		deadline.Store(end.UnixMilli())
		phase.Store(4)
		for !seen.Load() && time.Now().Before(end) {
			time.Sleep(20 * time.Millisecond)
		}
		if !seen.Load() {
			t.Fatal("browser artifact/acknowledgement deadline expired after Restore; not a source outcome")
		}
		stopSource()
		if !bytes.Equal(baseline, runtimeProof()) {
			t.Fatal("runtime changed after artifact capture")
		}
		current, e := fs.ReviewSource(ctx, name)
		if e != nil || current.Scope != after.Scope || current.Entrypoint != after.Entrypoint || !reflect.DeepEqual(current.Revisions, after.Revisions) || !reflect.DeepEqual(current.Files, after.Files) || !bytes.Equal(current.Files["compose.pkl"], initial) {
			t.Fatal("authoritative source changed after acknowledgement", e)
		}
		verifyArchive()
		protectedView, e := fs.ReviewSource(ctx, protectedName)
		if e != nil || len(protectedView.Revisions) != 0 || !reflect.DeepEqual(protectedView.Files, map[string][]byte{"compose.pkl": initial, "dill.pkl": schema}) {
			t.Fatal("protected source changed or entered archive", e)
		}
		// No process owns this state now. A missing authoritative protection root
		// must refuse the real CLI startup, not recreate an empty allow-state.
		protection := filepath.Join(data, "secret-state")
		if e = os.Rename(protection, protection+"-retained"); e != nil {
			t.Fatal(e)
		}
		restartCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		restart := exec.CommandContext(restartCtx, binary, args...)
		restart.Env = process.Env
		isolateFixtureProcess(restart)
		restart.WaitDelay = time.Second
		restart.Cancel = func() error { killFixtureGroup(restart); return nil }
		out, restartErr := restart.CombinedOutput()
		killFixtureGroup(restart) // stop descendants retaining output after parent exit
		if restartCtx.Err() != nil || restartErr == nil || !bytes.Contains(out, []byte("source history requires the existing private protection-state directory")) {
			t.Fatalf("missing protection root did not refuse startup: %v %s", restartErr, out)
		}
		if _, e = os.Lstat(protection); !os.IsNotExist(e) {
			t.Fatal("startup recreated missing protection state", e)
		}
		if !bytes.Equal(baseline, runtimeProof()) {
			t.Fatal("denied restart changed runtime")
		}
		db, e := sql.Open("sqlite", filepath.Join(data, "webhooks", "webhooks.db"))
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		var executions int
		if e = db.QueryRow("SELECT COUNT(*) FROM lifecycle_records").Scan(&executions); e != nil || executions != 0 {
			t.Fatal("Source Restore executed a runner operation", e, executions)
		}
		t.Log("production CLI source review/checkpoint/Restore preserved revisions; protected source denied and lost protection root refused restart without execution")
	}
	return proxy, run
}
