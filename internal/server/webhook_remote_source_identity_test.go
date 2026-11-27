package server

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"strings"
	"testing"
)

func TestRuntimeOnlyRemoteOperationNeverAdoptsSameNamedWorkspace(t *testing.T) {
	s, fs, _, _ := dispatcherServer(t)
	st, e := fs.Get("example")
	if e != nil {
		t.Fatal(e)
	}
	o, e := s.newRemoteExecutionObservation(context.Background(), remoteui.ExecutionMetadata{Host: "other-target", Target: strings.Repeat("a", 64), Stack: "example", Action: "remove", Request: "synthetic", RuntimeOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = o.Started(); e != nil {
		t.Fatal(e)
	}
	o.Finished(agentclient.DeploymentStatus{Stack: "example", RequestID: "synthetic", State: "succeeded", Effects: "complete"}, nil, true)
	events := journalEvents(t, s)
	if len(events) != 1 || events[0].Source.Kind != "unavailable" || events[0].Stack.ID == webhookStackID(st) {
		t.Fatal("runtime-only name collision acquired local source association", events)
	}
}
