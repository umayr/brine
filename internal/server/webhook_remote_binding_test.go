package server

import (
	"context"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/webhooks"
	"strings"
	"testing"
)

func TestRemoteObservationRejectsForeignRequestAndStack(t *testing.T) {
	for _, wrong := range []string{"request", "stack"} {
		s := webhookServer(t)
		svc, e := webhooks.OpenExisting(webhooks.Config{Directory: s.cfg.WebhooksDir, EncryptionKey: []byte(strings.Repeat("\x04", 32))})
		if e != nil {
			t.Fatal(e)
		}
		s.webhooks = svc
		o, e := s.newRemoteExecutionObservation(context.Background(), remoteui.ExecutionMetadata{Host: "target", Target: strings.Repeat("a", 64), Stack: "demo", Request: "request", Action: "deploy"})
		if e != nil {
			t.Fatal(e)
		}
		o.Started()
		status := agentclient.DeploymentStatus{RequestID: "request", Stack: "demo", State: "succeeded", Effects: "complete"}
		if wrong == "request" {
			status.RequestID = "foreign"
		} else {
			status.Stack = "foreign"
		}
		o.Finished(status, nil, true)
		events := journalEvents(t, s)
		if len(events) != 1 || events[0].Type != "operation.outcome_unknown" || events[0].Effects != "unknown" {
			t.Fatal("foreign fact laundered into completion", events)
		}
		svc.Close()
	}
}
