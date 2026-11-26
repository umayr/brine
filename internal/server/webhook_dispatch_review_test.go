package server

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/webhooks"
)

type firstLookupFailure struct {
	*store.FSStore
	calls int
}

func (s *firstLookupFailure) List() ([]*store.Stack, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errors.New("transient metadata failure")
	}
	return s.FSStore.List()
}
func TestDispatcherNeverAdmitsUsingAnEmptyLockIdentity(t *testing.T) {
	s, st, _, logPath := dispatcherServer(t)
	stack, _ := st.Get("example")
	wrapped := &firstLookupFailure{FSStore: st}
	s.store = wrapped
	d := s.webhookDispatcher.(*webhookDispatcher)
	result := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "restart"}, webhooks.IncomingRequest{ID: "req_initial_lookup_failure"})
	if result.Status != "rejected" || wrapped.calls != 1 {
		t.Fatal("transient lookup led to unsafe admission", result, wrapped.calls)
	}
	if _, e := os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("lookup failure executed child")
	}
}
func TestOperationJournalCapacityRejectsBeforeExecution(t *testing.T) {
	s, st, _, logPath := dispatcherServer(t)
	db, e := sql.Open("sqlite", filepath.Join(s.cfg.WebhooksDir, "webhooks.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	// Synthetic state saturates the journal without invoking any executor.
	_, e = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000) INSERT INTO lifecycle_records(id,base,updated) SELECT 'synthetic_cap_'||x,'{}',0 FROM n`)
	if e != nil {
		t.Fatal(e)
	}
	stack, _ := st.Get("example")
	d := s.webhookDispatcher.(*webhookDispatcher)
	result := d.Execute(context.Background(), webhooks.Trigger{StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "restart"}, webhooks.IncomingRequest{ID: "req_capacity_denied"})
	if result.Status != "rejected" || result.Reason != "operation_journal_capacity" {
		t.Fatal("capacity falsely marked uncertain execution", result)
	}
	if _, e := os.Stat(logPath); !os.IsNotExist(e) {
		t.Fatal("capacity denial executed child")
	}
}
