package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/umayr/brine/internal/webhooks"
	"os"
	"path/filepath"
	"testing"
)

func TestIncomingLocalSuccessSurvivesTerminalStorageFailure(t *testing.T) {
	s, fs, _, log := dispatcherServer(t)
	stack, _ := fs.Get("example")
	d := s.webhookDispatcher.(*webhookDispatcher)
	db, e := sql.Open("sqlite", filepath.Join(s.cfg.WebhooksDir, "webhooks.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(`CREATE TRIGGER fail_terminal BEFORE UPDATE OF terminal ON lifecycle_records WHEN NEW.terminal IS NOT NULL BEGIN SELECT RAISE(FAIL,'synthetic terminal storage outage'); END`); e != nil {
		t.Fatal(e)
	}
	r := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "plan"}, webhooks.IncomingRequest{ID: "req_terminal_outage"})
	if r.Status != "unknown" || r.Outcome != "success" || r.Reason != "operation_history_unavailable" {
		t.Fatal("incoming known result overwritten", r)
	}
	if b, e := os.ReadFile(log); e != nil || len(b) == 0 {
		t.Fatal("runner did not execute", e)
	}
	var started int64
	var terminal []byte
	if e = db.QueryRow("SELECT started,terminal FROM lifecycle_records WHERE id=?", "req_terminal_outage").Scan(&started, &terminal); e != nil || started == 0 || len(terminal) != 0 {
		t.Fatal("unresolved operation evidence lost", e, started)
	}
}
func TestIncomingCancelledPlanReportsNoRuntimeEffects(t *testing.T) {
	s, fs, runner, log := dispatcherServer(t)
	stack, _ := fs.Get("example")
	d := s.webhookDispatcher.(*webhookDispatcher)
	calls := 0
	runner.BeforeLegacyAction = func(context.Context, string) error {
		calls++
		if calls == 3 {
			return context.Canceled
		}
		return nil
	}
	r := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "plan"}, webhooks.IncomingRequest{ID: "req_cancelled_plan"})
	if r.Status != "unknown" || r.Outcome != "unknown" || calls != 3 {
		t.Fatal("cancellation fixture", r, calls)
	}
	if _, e := os.Stat(log); !os.IsNotExist(e) {
		t.Fatal("cancelled plan invoked child", e)
	}
	db, e := sql.Open("sqlite", filepath.Join(s.cfg.WebhooksDir, "webhooks.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var raw []byte
	var event webhooks.Event
	if e = db.QueryRow("SELECT terminal FROM lifecycle_records WHERE id=?", r.OperationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &event); e != nil || event.Effects != "none" || event.Outcome != "unknown" {
		t.Fatal("plan invented runtime effects", event, e)
	}
}
