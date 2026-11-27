package webhooks

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type testDispatcher struct {
	validate func(Trigger) error
	execute  func(Trigger, IncomingRequest) TriggerResult
	calls    int
}

func (d *testDispatcher) Validate(_ context.Context, t Trigger) error {
	if d.validate != nil {
		return d.validate(t)
	}
	return nil
}
func (d *testDispatcher) Execute(_ context.Context, t Trigger, r IncomingRequest) TriggerResult {
	d.calls++
	if d.execute != nil {
		return d.execute(t, r)
	}
	return TriggerResult{Status: "completed", OperationID: r.ID, Outcome: "success"}
}
func testTrigger(t *testing.T, s *Service) (Trigger, string) {
	t.Helper()
	tr, key, e := s.SaveTrigger(context.Background(), Trigger{Name: "Release", StackID: "example", TargetID: "local", Action: "plan", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	return tr, key
}
func acceptRequest(s *Service, tr Trigger, key, body string, d TriggerDispatcher) (IncomingRequest, error) {
	ts := strconv.FormatInt(s.now().Unix(), 10)
	return s.Accept(context.Background(), tr.ID, ts, Sign([]byte(key), ts, []byte(body)), []byte(body), d)
}

func TestAuthenticatedIdempotentAcceptance(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	const body = `{"id":"release-1"}`
	r, e := acceptRequest(s, tr, key, body, d)
	if e != nil || r.Status != "accepted" || d.calls != 0 {
		t.Fatalf("accepted=%+v %v", r, e)
	}
	duplicate, e := acceptRequest(s, tr, key, body, nil)
	if e != nil || duplicate.ID != r.ID {
		t.Fatal("duplicate request not stable", e)
	}
	if _, e = acceptRequest(s, tr, key, `{ "id":"release-1"}`, d); !errors.Is(e, ErrConflict) {
		t.Fatal("payload collision accepted", e)
	}
	if _, e = acceptRequest(s, tr, key, `{"id":"release-2","action":"deploy"}`, d); !errors.Is(e, ErrInvalid) {
		t.Fatal("action override accepted", e)
	}
	if _, e = acceptRequest(s, tr, "wrong", body, d); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("bad authentication accepted")
	}
	ts := strconv.FormatInt(s.now().Add(-time.Hour).Unix(), 10)
	if _, e = s.Accept(context.Background(), tr.ID, ts, Sign([]byte(key), ts, []byte(body)), []byte(body), d); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("stale timestamp accepted")
	}
	if _, e = acceptRequest(s, tr, key, `{"id":"release-2"}`, d); !errors.Is(e, ErrBusy) {
		t.Fatal("overlapping request admitted")
	}
	if _, e = s.executeOne(context.Background(), d); e != nil {
		t.Fatal(e)
	}
	if d.calls != 1 {
		t.Fatal("execution not called once")
	}
	duplicate, e = acceptRequest(s, tr, key, body, d)
	if e != nil || duplicate.Status != "completed" {
		t.Fatal("outcome not retained", e)
	}
	s.executeOne(context.Background(), d)
	if d.calls != 1 {
		t.Fatal("duplicate execution")
	}
}
func TestRevocationAndRebinding(t *testing.T) {
	for _, mode := range []string{"disable", "delete", "rotate", "rebind"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := testService(t, 10)
			tr, key := testTrigger(t, s)
			d := &testDispatcher{}
			acceptRequest(s, tr, key, `{"id":"release-1"}`, d)
			switch mode {
			case "disable":
				tr.Enabled = false
				s.SaveTrigger(ctx, tr)
			case "delete":
				s.DeleteTrigger(ctx, tr.ID, tr.Version)
			case "rotate":
				next, e := s.RotateTriggerSecret(ctx, tr.ID, tr.Version)
				if e != nil || next == key {
					t.Fatal("not rotated")
				}
			case "rebind":
				tr.Action = "restart"
				_, next, e := s.SaveTrigger(ctx, tr)
				if e != nil || next == "" || next == key {
					t.Fatal("rebind reused authority")
				}
			}
			s.executeOne(ctx, d)
			if d.calls != 0 {
				t.Fatal("revoked grant executed")
			}
			rows, _ := s.Requests(ctx, tr.ID)
			if rows[0].Status != "cancelled" {
				t.Fatal("queue not cancelled")
			}
			if _, e := acceptRequest(s, tr, key, `{"id":"another"}`, d); e == nil {
				t.Fatal("revoked key accepted")
			}
		})
	}
}
func TestExecutionRevalidationAndUnknownRecovery(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	acceptRequest(s, tr, key, `{"id":"restart-crash"}`, d)
	claimed, _, e := s.claimIncoming(ctx)
	if e != nil || claimed.Status != "running" {
		t.Fatal("claim failed", e)
	}
	if e = s.recoverIncoming(ctx); e != nil {
		t.Fatal(e)
	}
	rows, _ := s.Requests(ctx, tr.ID)
	if rows[0].Status != "unknown" || rows[0].Reason != "restart_requires_inspection" {
		t.Fatal("crash inferred a result")
	}
	s.executeOne(ctx, d)
	if d.calls != 0 {
		t.Fatal("uncertain action replayed")
	}
	if _, e := acceptRequest(s, tr, key, `{"id":"blocked-by-unknown"}`, d); !errors.Is(e, ErrBusy) {
		t.Fatal("unknown binding released", e)
	}
	if e := s.ResolveRequest(ctx, rows[0].ID, rows[0].UpdatedAt); e != nil {
		t.Fatal(e)
	}
	acceptRequest(s, tr, key, `{"id":"revalidate"}`, d)
	d.validate = func(Trigger) error { return errors.New("unreadable protected state") }
	s.executeOne(ctx, d)
	if d.calls != 0 {
		t.Fatal("admission bypass")
	}
	rows, _ = s.Requests(ctx, tr.ID)
	found := false
	for _, r := range rows {
		if r.IdempotencyKey == "revalidate" {
			found = r.Status == "rejected"
		}
	}
	if !found {
		t.Fatal("denial not retained")
	}
}
func TestConcurrentDeliveryQueuesExactlyOnce(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := acceptRequest(s, tr, key, `{"id":"shared-key"}`, d)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate queue records")
		}
	}
	rows, e := s.Requests(context.Background(), tr.ID)
	if e != nil || len(rows) != 1 {
		t.Fatal("not exactly one acceptance", e)
	}
}
func TestRateExpiryAndUnsupported(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	tr.RequestsPerMinute = 1
	tr, _, _ = s.SaveTrigger(ctx, tr)
	d := &testDispatcher{}
	if _, e := acceptRequest(s, tr, key, `{"id":"unsupported"}`, nil); !errors.Is(e, ErrUnsupported) {
		t.Fatal("missing dispatcher accepted")
	}
	acceptRequest(s, tr, key, `{"id":"first"}`, d)
	s.executeOne(ctx, d)
	if _, e := acceptRequest(s, tr, key, `{"id":"second"}`, d); !errors.Is(e, ErrRateLimited) {
		t.Fatal("rate limit ignored", e)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
	if _, e := acceptRequest(s, tr, key, `{"id":"expired"}`, d); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("expired grant accepted")
	}
}
func TestIngressIsNarrowAndHMACOnly(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	handler := s.Ingress(&testDispatcher{})
	body := `{"id":"http-one"}`
	ts := strconv.FormatInt(s.now().Unix(), 10)
	for _, tc := range []struct {
		method, path, signature string
		want                    int
	}{{"POST", "/hooks/" + tr.ID, "", 401}, {"GET", "/hooks/" + tr.ID, "", 404}, {"POST", "/hooks/" + tr.ID + "/extra", "", 404}, {"POST", "/hooks/" + tr.ID + "?action=deploy", "", 404}, {"POST", "/hooks/" + tr.ID, Sign([]byte(key), ts, []byte(body)), 202}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Brine-Timestamp", ts)
		r.Header.Set("Brine-Signature", tc.signature)
		r.Header.Set("Authorization", "Bearer unrelated")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
