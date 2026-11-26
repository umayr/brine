package web

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/store"
)

type unreadableAfterSave struct{ store.StackStore }

func (unreadableAfterSave) Get(string) (*store.Stack, error) {
	return nil, errors.New("synthetic metadata read failed")
}

func TestSourceSaveCheckpointReportsUncertainHistory(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status store.SourceCheckpointStatus
	}{
		{nil, ""},
		{store.ErrSourceCheckpointDisabled, ""},
		{store.ErrSourceUnavailable, store.CheckpointNotRetained},
		{fmt.Errorf("publish: %w", store.ErrSourceUncertain), store.CheckpointUnconfirmed},
		{store.ErrSourceInterrupted, store.CheckpointUnconfirmed},
	} {
		if got := checkpointStatus(tc.err); got != tc.status {
			t.Fatalf("%v reported %q, want %q", tc.err, got, tc.status)
		}
	}
	h := &Handler{store: unreadableAfterSave{}, sourceHistory: &sourceHistoryAccess{}}
	observe, release := h.BeginSourceSave(httptest.NewRequest("PUT", "/web/stacks/demo", nil))
	defer release()
	status := observe("demo")
	if status != store.CheckpointUnconfirmed || !strings.Contains(checkpointWarning(status), "could not be confirmed") {
		t.Fatalf("unreadable policy reported %q", status)
	}
	response := httptest.NewRecorder()
	dest := checkpointNotice(response, status, "/stacks/demo/edit?file=demo.pkl")
	if response.Header().Get(store.SourceCheckpointHeader) != "unknown" || !strings.Contains(dest, "checkpoint=unknown") || !strings.Contains(dest, "file=demo.pkl") {
		t.Fatalf("lost warning or file selection: %q", dest)
	}
}
