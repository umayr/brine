//go:build linux

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/store"
)

func TestDurableStreamDisconnectIndependentGETNoReplay(t *testing.T) {
	durableStreamHistoryCase(t, false)
}

func TestDurableStreamTerminalPersistenceFaultNoMarkerKnownGETRestartUnknown(t *testing.T) {
	durableStreamHistoryCase(t, true)
}

func durableStreamHistoryCase(t *testing.T, terminalFault bool) {
	t.Helper()
	t.Setenv("PRIVATE_ENV_SENTINEL", "PRIVATE_ENV_SENTINEL")
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	home, e = filepath.EvalSymlinks(home)
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.MkdirTemp(home, ".brine-api-history-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	data, history, coord := filepath.Join(root, "data"), filepath.Join(root, "history"), filepath.Join(root, "coord")
	for _, p := range []string{data, history, coord} {
		if e = os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s, e := operationhistory.Open(history, data, coord)
	if e != nil {
		t.Fatal("positive native private HOME", e)
	}
	defer s.Close()
	st, e := store.NewFSStore(data)
	if e != nil {
		t.Fatal(e)
	}
	stack, e := st.Create("demo", "PRIVATE_SOURCE_SENTINEL")
	if e != nil {
		t.Fatal(e)
	}
	ready, release, calls := filepath.Join(root, "ready"), filepath.Join(root, "release"), filepath.Join(root, "calls")
	endpoint := "/synthetic/no-engine.sock"
	expected := operationhistory.Digest("dill-local-docker-v1\x00independent-daemon")
	receipt := dill.OperationReceipt{Version: dill.OperationVersion, OperationID: "op_" + strings.Repeat("a", 32), Operation: "stop", Stack: "demo", Target: &dill.OperationTarget{Engine: "docker", EndpointDigest: operationhistory.Digest("unix://" + endpoint), QualifiedIdentityDigest: expected}, Outcome: "succeeded", Effects: "complete", Resources: []dill.OperationResource{}, Errors: []dill.OperationDiagnostic{}}
	payload, _ := json.Marshal(receipt)
	discovery := `{"version":"dill.operation/1","operations":["validate","plan","diff","up","down","stop","start","restart","rm","teardown"],"engines":["docker","podman"],"features":["explicitLocalSelector","attended","redactedSummary","conservativeEffects","optInQualifiedLocalCoordination"],"qualifiedCoordinationOperations":["up","down","stop","start","restart","rm"],"qualifiedCoordinationEngines":["docker"],"qualifiedCoordinationLayouts":["native-local-docker-v1"]}`
	binary := filepath.Join(root, "fake-dill")
	script := fmt.Sprintf("#!/bin/sh\n[ -z \"$PRIVATE_ENV_SENTINEL\" ] || exit 4\nprintf 'call\\n' >> '%s'\ncase \"$1\" in\noperation-capabilities) printf '%%s\\n' '%s'; exit 0;;\nesac\nprintf ready > '%s'\nwhile [ ! -f '%s' ]; do /bin/sleep 0.01; done\nprintf '%%s\\n' '%s'\nprintf PRIVATE_STDERR_SENTINEL >&2\n", calls, discovery, ready, release, payload)
	if e = os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	runner := dill.New(binary)
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = endpoint
	runner.RequireOperationObserver = true
	runner.ResolveStack = func(p string) (string, error) {
		if p != stack.PklPath {
			return "", fmt.Errorf("untrusted stack")
		}
		return "demo", nil
	}
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: coord, Layout: "native-local-docker-v1", DaemonID: "independent-daemon"}
	ag := agent.NewLocalAgent(nil, runner)
	authn, e := auth.New(auth.Config{User: "trusted-operator", Password: "PRIVATE_PASSWORD_SENTINEL", APIToken: "PRIVATE_TOKEN_SENTINEL"})
	if e != nil {
		t.Fatal(e)
	}
	r := chi.NewRouter()
	r.Use(authn.Middleware)
	Mount(r, ag, st, nil, nil, "docker", httpx.Timeouts{}, 10*time.Second, s)
	server := httptest.NewServer(r)
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/api/stacks/demo/stop", nil)
	req.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_SENTINEL")
	response, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	id := response.Header.Get("X-Brine-Request-ID")
	if !operationhistory.ValidID(id) || response.Header.Get("Location") != "/api/operations/"+id {
		t.Fatal("no stable early id")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e = os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake child did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	disk, e := os.ReadFile(filepath.Join(history, id+".json"))
	if e != nil || !strings.Contains(string(disk), `"phase":"running"`) {
		t.Fatal("child without durable accepted+started record", e)
	}
	defer response.Body.Close()
	if !terminalFault {
		response.Body.Close()
	} // Independent client, no cancellation of engine owner.
	lookup := func() operationhistory.Record {
		get, _ := http.NewRequest("GET", server.URL+"/api/operations/"+id, nil)
		get.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_SENTINEL")
		res, e := http.DefaultClient.Do(get)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		bytes, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != 200 {
			t.Fatalf("lookup: %d %s %v", res.StatusCode, bytes, e)
		}
		for _, sentinel := range []string{"PRIVATE_", root, endpoint, binary, coord} {
			if strings.Contains(string(bytes), sentinel) {
				t.Fatal("public leak", sentinel)
			}
		}
		var v operationhistory.Record
		if json.Unmarshal(bytes, &v) != nil || v.RequestID != id {
			t.Fatal("invalid lookup")
		}
		return v
	}
	if v := lookup(); v.Phase != "running" || v.DillOperationID != nil {
		t.Fatal("invented running completion")
	}
	// Inject only after a real positive accepted/running/GET and child-ready
	// control. Unsafe root mode makes terminal persistence fail before rename.
	if terminalFault {
		if e = os.Chmod(history, 0500); e != nil {
			t.Fatal(e)
		}
		unavailable, _ := http.NewRequest("GET", server.URL+"/api/operations/"+id, nil)
		unavailable.Header.Set("Authorization", "Bearer PRIVATE_TOKEN_SENTINEL")
		interim, e := http.DefaultClient.Do(unavailable)
		if e != nil {
			t.Fatal(e)
		}
		interim.Body.Close()
		if interim.StatusCode != http.StatusServiceUnavailable {
			t.Fatal("unsafe running history did not fail closed", interim.StatusCode)
		}
	}
	if e = os.WriteFile(release, []byte("release"), 0600); e != nil {
		t.Fatal(e)
	}
	if terminalFault {
		// While the injected root is unsafe and the child is still running,
		// lookup correctly returns 503. Wait for the completed stream before
		// requiring the known terminal failed_inspect overlay to exist.
		body, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(body), dill.OperationMarker) {
			t.Fatal("failed history emitted terminal marker", string(body))
		}
		if !strings.Contains(string(body), "[brine:error]") || !strings.Contains(string(body), "Operation history persistence failed") {
			t.Fatal("fixed history error missing", string(body))
		}
	}
	for {
		v := lookup()
		if v.Phase == "terminal" {
			if v.Invocation.Receipt.Outcome != "succeeded" || v.Invocation.Receipt.Effects != "complete" || v.Status != "succeeded" || v.Invocation.ExitCode != 0 {
				t.Fatal("transport/history failure changed engine axes")
			}
			if terminalFault && (v.Persistence != "failed_inspect" || v.Reason != "history_failure_requires_inspection" || v.DillOperationID == nil || *v.DillOperationID != receipt.OperationID) {
				t.Fatal("known receipt failure lookup lost", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached invocation did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	finalRecord, e := os.ReadFile(filepath.Join(history, id+".json"))
	if e != nil || strings.Contains(string(finalRecord), "PRIVATE_") {
		t.Fatal("private history sentinel leak", e)
	}
	if terminalFault {
		var pending operationhistory.Record
		if json.Unmarshal(finalRecord, &pending) != nil || pending.Phase != "running" || pending.Invocation != nil || pending.DillOperationID != nil {
			t.Fatal("invented durable terminal after failed commit", string(finalRecord))
		}
		// Restore the injected test-only mode to exercise restart of the unchanged
		// pending durable record; production never repairs this root automatically.
		if e = os.Chmod(history, 0700); e != nil {
			t.Fatal(e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		restarted, e := operationhistory.Open(history, data, coord)
		if e != nil {
			t.Fatal(e)
		}
		defer restarted.Close()
		recovered, e := restarted.Get(id)
		if e != nil || recovered.Phase != "unknown" || recovered.Status != "unknown" || recovered.Reason != "restart_requires_inspection" || recovered.Invocation != nil || recovered.DillOperationID != nil || recovered.EndObservedAt != nil || recovered.RecoveryObservedAt == nil {
			t.Fatal("restart fabricated terminal", recovered, e)
		}
	}
	bytes, e := os.ReadFile(calls)
	if e != nil || strings.Count(string(bytes), "call") != 2 {
		t.Fatal("lookup replayed POST or discovery", string(bytes), e)
	}
	for _, noauth := range []bool{true, false} {
		a, e := auth.New(auth.Config{NoAuth: noauth, User: "trusted-operator", Password: "synthetic", APIToken: "synthetic"})
		if e != nil {
			t.Fatal(e)
		}
		synthetic := chi.NewRouter()
		synthetic.Use(a.Middleware)
		Mount(synthetic, ag, st, nil, nil, "docker", httpx.Timeouts{}, time.Second, s)
		w := httptest.NewRecorder()
		synthetic.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/api/operations/"+id, nil))
		if w.Code != 401 {
			t.Fatal("unauthenticated lookup accepted", noauth, w.Code)
		}
	}
}
