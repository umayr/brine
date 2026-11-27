package web

import (
	"bytes"
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/brokerwire"
	"github.com/umayr/brine/internal/configvars"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/store"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerCaptureBindsSourceMetadataAndEffectiveVariables(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := st.Create("demo", "config {}\n")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := configvars.New(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	runner := dill.New("/not-executed")
	runner.Variables = vars
	h := New(agent.NewLocalAgent(nil, runner), st, nil, nil, "docker", false, nil, 0)
	first, digest, err := h.brokerCapture(context.Background(), "demo")
	if err != nil || len(first.Files) < 2 {
		t.Fatal("capture unavailable", err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(stack.PklPath), "extra.txt"), []byte("synthetic source"), 0600); err != nil {
		t.Fatal(err)
	}
	_, changed, err := h.brokerCapture(context.Background(), "demo")
	if err != nil || changed == digest {
		t.Fatal("source drift not bound")
	}
	if err = vars.Set("demo", "DOMAIN", "example.test", false); err != nil {
		t.Fatal(err)
	}
	withVars, variableDigest, err := h.brokerCapture(context.Background(), "demo")
	if err != nil || variableDigest == changed {
		t.Fatal("variable change not source-bound", err)
	}
	found := false
	for _, f := range withVars.Files {
		if f.Path == brokerwire.InputsFile {
			input, e := brokerwire.DecodeInputs(bytes.NewReader(f.Content))
			if e != nil || input.Variables["BRINE_VAR_DOMAIN"] != "example.test" {
				t.Fatal("effective variables missing")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("input document missing")
	}
}
func TestBrokerPlanMutationRoutesFailClosedWithoutBrowserOrPolicy(t *testing.T) {
	h := New(nil, nil, nil, nil, "docker", false, nil, 0)
	router := chi.NewRouter()
	Mount(router, h, httpx.Timeouts{})
	for _, action := range []string{"prepare", "confirm", "status"} {
		r := httptest.NewRequest("POST", "/web/stacks/demo/broker/"+action, strings.NewReader("review=untrusted"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(action, w.Code)
		}
	}
}
