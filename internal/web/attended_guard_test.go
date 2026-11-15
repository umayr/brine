package web

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/api"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
)

func TestAttendedWebGuardDeniesLegacyEvaluationAndMutation(t *testing.T) {
	runner := dill.New("/must-not-execute")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/synthetic/socket"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
	ag := agent.NewLocalAgent(&dashboardEngine{}, runner)
	boundary, err := api.NewAttendedContext(ag, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(ag, nil, nil, nil, "docker", false, nil, time.Second)
	r := chi.NewRouter()
	if err := MountAttended(r, h, httpx.Timeouts{}, boundary); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/web/stacks/demo/plan"}, {"GET", "/web/stacks/demo/containers"}, {"POST", "/web/stacks/demo/validate"}, {"POST", "/web/stacks/demo/fmt"}, {"POST", "/web/stacks/demo/git/pull"}, {"POST", "/web/containers/id/start"}, {"POST", "/web/images/pull"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != 503 {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
