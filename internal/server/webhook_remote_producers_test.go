package server

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/remoteui"
	"github.com/umayr/brine/internal/webhooks"
	"strings"
	"testing"
)

func TestRemoteProducerPreservesReportedUnavailableAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		state, effects, kind, outcome string
		action                        string
		sent                          bool
	}{{"succeeded", "complete", "operation.completed", "success", "deploy", true}, {"failed", "partial", "operation.completed", "failure", "deploy", true}, {"unknown", "unknown", "operation.outcome_unknown", "unknown", "deploy", true}, {"not-admitted", "none", "operation.rejected", "unavailable", "remove", true}, {"", "", "operation.rejected", "unavailable", "deploy", false}, {"", "", "operation.completed", "success", "plan", true}} {
		s := webhookServer(t)
		svc, e := webhooks.OpenExisting(webhooks.Config{Directory: s.cfg.WebhooksDir, EncryptionKey: []byte(strings.Repeat("\x04", 32))})
		if e != nil {
			t.Fatal(e)
		}
		s.webhooks = svc
		o, e := s.newRemoteExecutionObservation(context.Background(), remoteui.ExecutionMetadata{Host: "synthetic", Target: strings.Repeat("a", 64), Stack: "unregistered", Action: tc.action, Request: "synthetic-request"})
		if e != nil {
			t.Fatal(e)
		}
		if tc.sent {
			if e = o.Started(); e != nil {
				t.Fatal(e)
			}
		}
		status := agentclient.DeploymentStatus{State: tc.state, Effects: tc.effects, Stack: "unregistered"}
		if tc.state != "" {
			status.RequestID = "synthetic-request"
		}
		var rpcError error
		if tc.state == "unknown" || !tc.sent {
			rpcError = errors.New("unavailable")
		}
		o.Finished(status, rpcError, tc.sent)
		events := journalEvents(t, s)
		if len(events) != 1 || events[0].Type != tc.kind || events[0].Outcome != tc.outcome || events[0].Source.Kind != "unavailable" || events[0].Target.Kind != "remote" {
			t.Fatal("remote facts rewritten", tc, events)
		}
		svc.Close()
	}
}
