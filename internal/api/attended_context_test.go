package api

import (
	"testing"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
)

func TestAttendedContextRequiresAlphaBeforeAgentAndSharedRunner(t *testing.T) {
	makeRunner := func() *dill.Runner {
		r := dill.New("fake")
		r.Profile = "alpha"
		r.Engine = "docker"
		r.Socket = "/synthetic/socket"
		r.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
		return r
	}
	r := makeRunner()
	a := agent.NewLocalAgent(&secretRouteEngine{}, r)
	configured, err := NewAttendedContext(a, r, nil)
	if err != nil || configured == nil || configured.producer != nil {
		t.Fatalf("configured unavailable seam: %v", err)
	}
	if _, err := NewAttendedContext(nil, r, nil); err == nil {
		t.Fatal("nil agent accepted")
	}
	if _, err := NewAttendedContext(a, nil, nil); err == nil {
		t.Fatal("nil runner accepted")
	}
	if _, err := NewAttendedContext(a, makeRunner(), nil); err == nil {
		t.Fatal("different shared runner accepted")
	}
	r2 := makeRunner()
	r2.Profile = "local"
	nonAlpha := agent.NewLocalAgent(&secretRouteEngine{}, r2)
	if _, err := NewAttendedContext(nonAlpha, r2, nil); err == nil {
		t.Fatal("non-Alpha accepted")
	}
	r2.Profile = "alpha"
	if _, err := NewAttendedContext(nonAlpha, r2, nil); err == nil {
		t.Fatal("late Alpha without read-only engine accepted")
	}
	r3 := makeRunner()
	r3.QualifiedOperation = nil
	if _, err := NewAttendedContext(agent.NewLocalAgent(&secretRouteEngine{}, r3), r3, nil); err == nil {
		t.Fatal("missing qualified shape accepted")
	}
}
