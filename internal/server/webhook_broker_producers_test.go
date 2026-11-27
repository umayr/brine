package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/store"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBrokerProducerReportsOnlyAllowlistedFacts(t *testing.T) {
	for _, tc := range []struct {
		state, effects, action, kind, outcome string
		sent                                  bool
	}{
		{"succeeded", "complete", "deploy", "operation.completed", "success", true},
		{"succeeded", "none", "deploy", "operation.completed", "success", true},
		{"failed", "partial", "deploy", "operation.completed", "failure", true},
		{"unknown", "unknown", "deploy", "operation.outcome_unknown", "unknown", true},
		{"", "", "deploy", "operation.rejected", "unavailable", false},
		{"", "", "plan", "operation.completed", "success", true},
	} {
		t.Run(tc.action+tc.state, func(t *testing.T) {
			s, _, _, _ := dispatcherServer(t)
			meta := brokerapp.ExecutionMetadata{Stack: "example", Target: "reviewed-target", TargetKind: "local", TargetDigest: strings.Repeat("a", 64), Action: tc.action, Request: strings.Repeat("b", 32)}
			if tc.action == "plan" {
				meta.Request = ""
			}
			o, err := s.newBrokerExecutionObservation(context.Background(), meta)
			if err != nil {
				t.Fatal(err)
			}
			if tc.sent && o.Started() != nil {
				t.Fatal("start failed")
			}
			status := brokerapp.Outcome{RequestID: meta.Request, Stack: meta.Stack, State: tc.state, Effects: tc.effects}
			var outcomeErr error
			if tc.state == "unknown" || !tc.sent {
				outcomeErr = errors.New("private receiver/source detail must not be stored")
			}
			o.Finished(status, outcomeErr, tc.sent)
			events := journalEvents(t, s)
			if len(events) != 1 || events[0].Type != tc.kind || events[0].Outcome != tc.outcome || events[0].Source.Kind != "workspace" || events[0].Target.Kind != "local" || events[0].Target.Name != meta.Target {
				t.Fatal("broker facts changed", events)
			}
			if tc.action == "plan" && events[0].Effects != "none" {
				t.Fatal("Plan invented runtime effects")
			}
			if tc.action == "deploy" && tc.sent && events[0].Effects != tc.effects {
				t.Fatal("raw effects changed")
			}
			raw, marshalErr := json.Marshal(events)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(raw), "private receiver") || strings.Contains(string(raw), meta.TargetDigest) {
				t.Fatal("unsafe metadata escaped")
			}
		})
	}
}
func TestBrokerProducerIgnoresForeignReceipt(t *testing.T) {
	s, _, _, _ := dispatcherServer(t)
	o, err := s.newBrokerExecutionObservation(context.Background(), brokerapp.ExecutionMetadata{Stack: "example", Target: "remote-alias", TargetKind: "remote", TargetDigest: "digest", Action: "deploy", Request: "request"})
	if err != nil || o.Started() != nil {
		t.Fatal(err)
	}
	o.Finished(brokerapp.Outcome{RequestID: "foreign", Stack: "example", State: "succeeded", Effects: "complete"}, nil, true)
	events := journalEvents(t, s)
	if len(events) != 1 || events[0].Outcome != "unknown" || events[0].Target.Kind != "remote" {
		t.Fatal("foreign success accepted", events)
	}

}

func TestBrokerProducerGitSourceIsIndependentOfLocalTarget(t *testing.T) {
	s, fs, _, _ := dispatcherServer(t)
	repo, err := fs.RepoDir("git-example")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "compose.pkl"), []byte("synthetic source metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.CreateFromGit("git-example", &store.GitSource{URL: "https://example.invalid/repo.git", Branch: "main"}, "compose.pkl"); err != nil {
		t.Fatal(err)
	}
	o, err := s.newBrokerExecutionObservation(context.Background(), brokerapp.ExecutionMetadata{Stack: "git-example", Target: "alias", TargetKind: "local", TargetDigest: "digest", Action: "deploy", Request: "request"})
	if err != nil || o.Started() != nil {
		t.Fatal(err)
	}
	o.Finished(brokerapp.Outcome{RequestID: "request", Stack: "other", State: "succeeded", Effects: "complete"}, nil, true)
	events := journalEvents(t, s)
	if len(events) != 1 || events[0].Source.Kind != "git" || events[0].Target.Kind != "local" || events[0].Outcome != "unknown" {
		t.Fatal("source guessed from target or foreign stack accepted", events)
	}
}

type brokerLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *brokerLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *brokerLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func TestBrokerProducerTerminalStorageFailureIsDiagnostic(t *testing.T) {
	s, _, _, _ := dispatcherServer(t)
	o, err := s.newBrokerExecutionObservation(context.Background(), brokerapp.ExecutionMetadata{Stack: "example", Target: "alias", TargetKind: "local", TargetDigest: "digest", Action: "deploy", Request: "request"})
	if err != nil || o.Started() != nil {
		t.Fatal(err)
	}
	var messages brokerLogBuffer
	old := log.Writer()
	log.SetOutput(&messages)
	defer log.SetOutput(old)
	s.webhooks.Close()
	// This is the FIRST terminal write for a fresh started observation.
	o.Finished(brokerapp.Outcome{RequestID: "request", Stack: "example", State: "succeeded", Effects: "complete"}, nil, true)
	if !strings.Contains(messages.String(), "Remote execution outcome could not be journaled") {
		t.Fatal("missing failed-write diagnostic")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.cfg.WebhooksDir, "webhooks.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var records int
	if err = db.QueryRow("SELECT count(*) FROM lifecycle_records WHERE started>0 AND terminal IS NULL").Scan(&records); err != nil || records != 1 {
		t.Fatal("failed write fabricated terminal fact", records, err)
	}
}
