//go:build unix

package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
)

// Producer-shaped terminal emulation exercises the API stream boundary without
// running a candidate, opening a socket or weakening the native Linux gate.
func TestQualifiedStreamPreservesProducerReceiptAndProcessFailure(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		receipt := &dill.OperationReceipt{Version: dill.OperationVersion, OperationID: "op_" + strings.Repeat("a", 32), Operation: "stop", Stack: "demo", Outcome: "succeeded", Effects: "complete", Target: &dill.OperationTarget{Engine: "docker", EndpointDigest: "sha256:" + strings.Repeat("b", 64), QualifiedIdentityDigest: "sha256:" + strings.Repeat("c", 64)}, Resources: []dill.OperationResource{{Kind: "container", IdentityDigest: "sha256:" + strings.Repeat("d", 64), Action: "stop", Effect: "complete"}}, Errors: []dill.OperationDiagnostic{}}
		invocation := dill.OperationInvocation{Receipt: receipt, ExitCode: 0, Status: "succeeded"}
		var actionError error
		if cleanupFailure {
			receipt.Errors = []dill.OperationDiagnostic{{Code: "COORDINATION_RELEASE_FAILED", Message: "Engine outcome was recorded; local coordination cleanup failed."}}
			invocation.ExitCode = 1
			invocation.Status = "failed"
			actionError = &dill.OperationError{Invocation: invocation}
		}
		payload, err := json.Marshal(invocation)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		fmt.Fprintln(w, dill.OperationMarker+string(payload))
		writeActionStatus(w, "stop", actionError)
		first := strings.Split(w.Body.String(), "\n")[0]
		decoded, err := dill.DecodeOperationInvocation([]byte(strings.TrimPrefix(first, dill.OperationMarker)))
		if err != nil || decoded.Status != invocation.Status || decoded.Receipt.Outcome != "succeeded" || decoded.Receipt.Effects != "complete" {
			t.Fatalf("receipt axes lost: %+v %v", decoded, err)
		}
		if strings.Contains(w.Body.String(), "[brine:done]") == cleanupFailure {
			t.Fatalf("overall process failure hidden: %s", w.Body.String())
		}
	}
}

func TestQualifiedRoutesRequireAuthAndDoNotDeleteOrUseSecrets(t *testing.T) {
	st, err := store.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Create("demo", `config { engine = "docker" }`); err != nil {
		t.Fatal(err)
	}
	runner := dill.New("/must-not-run")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/must-not-open.sock"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/must-not-open", Layout: "native-local-docker-v1", DaemonID: "independently-known-daemon"}
	ag := agent.NewLocalAgent(nil, runner)
	for _, noauth := range []bool{true, false} {
		a, e := auth.New(auth.Config{NoAuth: noauth, User: "test", Password: "synthetic-password", APIToken: "synthetic-token"})
		if e != nil {
			t.Fatal(e)
		}
		router := chi.NewRouter()
		router.Use(a.Middleware)
		Mount(router, ag, st, nil, nil, "docker", httpx.Timeouts{}, time.Second)
		for _, tc := range []struct {
			method, path, body string
			want               int
		}{
			{"POST", "/api/stacks/demo/up", "", 401},
			{"POST", "/api/stacks/demo/down", "", 401},
			{"POST", "/api/stacks/demo/stop", "", 401},
			{"POST", "/api/stacks/demo/start", "", 401},
			{"POST", "/api/stacks/demo/restart", "", 401},
			{"POST", "/api/stacks/demo/teardown", "", 401},
			{"GET", "/api/stacks/demo/plan", "", 401},
			{"GET", "/api/stacks/demo/validate", "", 401},
		} {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("noauth=%v %s got %d %s", noauth, tc.path, w.Code, w.Body.String())
			}
		}
		if noauth {
			continue
		}
		for _, tc := range []struct {
			method, path, body string
			want               int
		}{
			{"DELETE", "/api/stacks/demo", `{"remove_containers":true}`, 409},
			{"DELETE", "/api/stacks/demo", `{"remove_containers":false}`, 409},
			{"GET", "/api/stacks/demo/secret-bindings", "", 503},
			{"POST", "/api/stacks/demo/secret-bindings/confirm", `{"preview_token":"` + strings.Repeat("A", 43) + `"}`, 503},
		} {
			req := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer synthetic-token")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("%s got %d %s", tc.path, w.Code, w.Body.String())
			}
		}
	}
	if _, err = st.Get("demo"); err != nil {
		t.Fatalf("qualified metadata deleted: %v", err)
	}
}
