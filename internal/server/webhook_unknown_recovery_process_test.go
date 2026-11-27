package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/store"
	"github.com/umayr/brine/internal/web"
	"github.com/umayr/brine/internal/webhooks"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func qualifyUnknownRecovery(t *testing.T, ctx context.Context, s *Server, fs *store.FSStore, svc *webhooks.Service, stack, base, csrf string, operator *http.Client, option web.WebhookOption, relay *responseLossRelay, restart webhooks.Trigger, restartSecret string, inspect func() (bool, string)) {
	t.Helper()
	before, e := fs.Get(stack)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(webhooks.Trigger{Name: "lost response stop", Enabled: true, StackID: option.StackID, TargetID: option.TargetID, Action: "stop"})
	request, _ := http.NewRequestWithContext(ctx, "POST", base+"/api/webhooks/triggers", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response, e := operator.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	var created struct {
		Trigger webhooks.Trigger `json:"trigger"`
		Secret  string           `json:"secret"`
	}
	json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	if response.StatusCode != 201 || created.Secret == "" {
		t.Fatal("cannot create response-loss binding")
	}
	send := func(trigger webhooks.Trigger, secret, id string) int {
		b, _ := json.Marshal(map[string]string{"id": id})
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		r, _ := http.NewRequestWithContext(ctx, "POST", base+"/hooks/"+trigger.ID, bytes.NewReader(b))
		r.Header.Set("Brine-Timestamp", ts)
		r.Header.Set("Brine-Signature", webhooks.Sign([]byte(secret), ts, b))
		r.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	wait := func(trigger, id, status string) webhooks.IncomingRequest {
		end := time.Now().Add(40 * time.Second)
		for time.Now().Before(end) {
			rows, e := svc.Requests(ctx, trigger)
			if e != nil {
				t.Fatal(e)
			}
			for _, row := range rows {
				if row.IdempotencyKey == id && row.Status == status {
					return row
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("request did not reach expected state", id, status)
		return webhooks.IncomingRequest{}
	}
	stops, restarts := relay.Stops.Load(), relay.Restarts.Load()
	relay.Arm.Store(true)
	if send(created.Trigger, created.Secret, "lost-stop-response") != 202 {
		t.Fatal("loss request not accepted")
	}
	unknown := wait(created.Trigger.ID, "lost-stop-response", "unknown")
	if !relay.Dropped.Load() || relay.Invalid.Load() || relay.Stops.Load() != stops+1 {
		t.Fatal("fault did not discard exactly one actual successful stop response")
	}
	if running, _ := inspect(); running {
		t.Fatal("target stop did not actually execute")
	}
	fresh, _ := fs.Get(stack)
	if fresh.RemoteGit.DeploymentRequest != before.RemoteGit.DeploymentRequest {
		t.Fatal("unconfirmed target outcome advanced reviewed head")
	}
	rows, e := fs.ListManualIntents()
	if e != nil {
		t.Fatal(e)
	}
	intent := store.ManualDeploymentIntent{}
	for _, row := range rows {
		if row.Stack == stack && !row.Resolved && row.Mode == "stop" {
			intent = row
		}
	}
	if intent.Request == "" {
		t.Fatal("unknown stop did not retain recovery")
	}
	if send(restart, restartSecret, "blocked-during-loss") != 409 || relay.Restarts.Load() != restarts {
		t.Fatal("unknown binding did not block conflicting execution")
	}
	if send(created.Trigger, created.Secret, "lost-stop-response") != 202 || relay.Stops.Load() != stops+1 {
		t.Fatal("stable duplicate replayed lost operation")
	}
	single := func(status string) {
		rows, e := svc.Requests(ctx, created.Trigger.ID)
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, r := range rows {
			if r.IdempotencyKey == "lost-stop-response" {
				count++
				if r.ID != unknown.ID || r.OperationID != unknown.OperationID || r.Outcome != "unknown" || r.Status != status {
					t.Fatal("stable request identity/outcome changed")
				}
			}
		}
		if count != 1 {
			t.Fatal("stable duplicate created multiple records", count)
		}
	}
	single("unknown")
	// Browser cannot begin recovery until every old-head/block/duplicate check
	// above is complete. It cannot acknowledge the queue until Go has observed
	// exact intent/head reconciliation while the queue is STILL unknown.
	relay.Phase.Store(1)
	reconciled := false
	for end := time.Now().Add(40 * time.Second); time.Now().Before(end); {
		saved, e := fs.GetManualIntent(intent.Request)
		if e == nil && saved.Resolved {
			fresh, _ := fs.Get(stack)
			if fresh.RemoteGit.DeploymentRequest != intent.Request || fresh.RemoteGit.DeploymentJournal != intent.Journal {
				t.Fatal("target inspection did not advance exact lineage")
			}
			single("unknown")
			if relay.Stops.Load() != stops+1 || relay.Restarts.Load() != restarts {
				t.Fatal("target inspection replayed operation")
			}
			reconciled = true
			relay.Phase.Store(2)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !reconciled {
		t.Fatal("browser did not reconcile target before queue acknowledgement")
	}
	resolved := wait(created.Trigger.ID, "lost-stop-response", "resolved")
	single("resolved")
	saved, e := fs.GetManualIntent(intent.Request)
	fresh, _ = fs.Get(stack)
	if e != nil || !saved.Resolved || fresh.RemoteGit.DeploymentRequest != intent.Request || resolved.Outcome != "unknown" || resolved.OperationID != unknown.OperationID {
		t.Fatal("inspection fabricated outcome or lost verified lineage")
	}
	if relay.Stops.Load() != stops+1 || relay.Restarts.Load() != restarts {
		t.Fatal("inspection replayed an operation")
	}
	if running, _ := inspect(); running {
		t.Fatal("inspection changed stopped target")
	}
	if send(restart, restartSecret, "after-inspected-loss") != 202 {
		t.Fatal("inspected binding did not resume")
	}
	completed := wait(restart.ID, "after-inspected-loss", "completed")
	if completed.Outcome != "success" || relay.Restarts.Load() != restarts+1 || relay.Stops.Load() != stops+1 {
		t.Fatal("new request did not execute exactly once")
	}
	if running, _ := inspect(); !running {
		t.Fatal("new restart did not start target")
	}
	found := false
	for _, event := range journalEvents(t, s) {
		if event.OperationID == unknown.OperationID {
			found = true
			if event.Type != "operation.outcome_unknown" || event.Outcome != "unknown" {
				t.Fatal("inspection rewrote immutable operation fact")
			}
		}
	}
	if !found {
		t.Fatal("unknown operation fact missing")
	}
	t.Log("real lost successful stop response stayed unknown; browser reconciled exact intent and acknowledged queue without replay; fresh restart succeeded")
}
