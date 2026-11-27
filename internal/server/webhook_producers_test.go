package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/webhooks"
)

func journalEvents(t *testing.T, s *Server) []webhooks.Event {
	t.Helper()
	db, e := sql.Open("sqlite", filepath.Join(s.cfg.WebhooksDir, "webhooks.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := db.Query("SELECT terminal FROM lifecycle_records ORDER BY id")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := []webhooks.Event{}
	for rows.Next() {
		var b []byte
		var event webhooks.Event
		if rows.Scan(&b) != nil || json.Unmarshal(b, &event) != nil {
			t.Fatal("incomplete operation journal")
		}
		out = append(out, event)
	}
	return out
}
func TestNormalAPIAndUIActionsPublishLifecycleJournal(t *testing.T) {
	s, _, _, logPath := dispatcherServer(t)
	s.configureWebhookProducers()
	server := httptest.NewServer(s.router)
	defer server.Close()
	for _, path := range []string{"/api/stacks/example/plan", "/web/stacks/example/plan", "/api/stacks/example/restart"} {
		method := "GET"
		if strings.HasSuffix(path, "restart") {
			method = "POST"
		}
		r, e := http.NewRequest(method, server.URL+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer fixture-token")
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("normal path %s: %d %s", path, res.StatusCode, body)
		}
	}
	events := journalEvents(t, s)
	if len(events) != 3 {
		t.Fatalf("normal routes not journaled once each: %+v", events)
	}
	for _, event := range events {
		if event.Type != "operation.completed" || event.Outcome != "success" || event.Stack.ID == "" || event.Source.Kind != "workspace" || event.Target.Kind != "local" {
			t.Fatalf("incorrect event: %+v", event)
		}
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Count(string(calls), "\n") != 3 {
		t.Fatal("observer changed execution count")
	}
}
func TestNormalDeniedActionRecordsRejectionWithoutChild(t *testing.T) {
	s, st, runner, logPath := dispatcherServer(t)
	stack, _ := st.Get("example")
	runner.BeforeLegacyAction = func(context.Context, string) error { return dill.ErrSecretUnavailable }
	s.configureWebhookProducers()
	_, e := runner.Plan(context.Background(), stack.PklPath)
	if e == nil {
		t.Fatal("protected source ran")
	}
	events := journalEvents(t, s)
	if len(events) != 1 || events[0].Type != "operation.rejected" || events[0].Effects != "none" {
		t.Fatalf("denial not journaled: %+v", events)
	}
	if _, e = os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("denial executed child")
	}
}
func TestIncomingActionDoesNotDoubleJournalGlobalObserver(t *testing.T) {
	s, st, _, _ := dispatcherServer(t)
	s.configureWebhookProducers()
	d := s.webhookDispatcher.(*webhookDispatcher)
	stack, _ := st.Get("example")
	result := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "restart"}, webhooks.IncomingRequest{ID: "req_already_observed"})
	if result.Status != "completed" {
		t.Fatal(result)
	}
	if events := journalEvents(t, s); len(events) != 1 {
		t.Fatalf("duplicate lifecycle journals: %+v", events)
	}
}
