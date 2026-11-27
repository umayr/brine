package webhooks

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReleaseBoundaryDestinationExclusions(t *testing.T) {
	n := deliveryNetwork{allowed: []netip.Prefix{netip.MustParsePrefix("fc00::/7")}}
	for _, raw := range []string{"::127.0.0.1", "::8.8.8.8", "fec0::1", "fd00:ec2::23", "3fff::1"} {
		if n.permits(netip.MustParseAddr(raw)) {
			t.Errorf("unsafe destination allowed: %s", raw)
		}
	}
}
func TestReleaseBoundaryExactIncomingID(t *testing.T) {
	for _, body := range []string{`{"ID":"upper"}`, `{"id":"first","id":"last"}`, `{"id":"first","ID":"last"}`} {
		s := testService(t, 10)
		tr, key := testTrigger(t, s)
		if _, e := acceptRequest(s, tr, key, body, &testDispatcher{}); !errors.Is(e, ErrInvalid) {
			t.Errorf("ambiguous id admitted: %s %v", body, e)
		}
	}
}
func TestReleaseBoundarySaturatedAdmissionDoesNotWait(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	for i := 0; i < cap(s.incomingWaiters); i++ {
		s.incomingWaiters <- struct{}{}
	}
	held := true
	defer func() {
		if held {
			for len(s.incomingWaiters) > 0 {
				<-s.incomingWaiters
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	body := []byte(`{"id":"saturated"}`)
	ts := strconv.FormatInt(s.now().Unix(), 10)
	connection, e := s.db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Close()
	_, e = s.Accept(ctx, tr.ID, ts, Sign([]byte(key), ts, body), body, &testDispatcher{})
	if !errors.Is(e, errAdmissionSaturated) || ctx.Err() != nil {
		t.Fatal("saturated admission waited for cancellation", e, ctx.Err())
	}
	r := httptest.NewRequest("POST", "/hooks/"+tr.ID, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Brine-Timestamp", ts)
	r.Header.Set("Brine-Signature", Sign([]byte(key), ts, body))
	w := httptest.NewRecorder()
	s.Ingress(&testDispatcher{}).ServeHTTP(w, r)
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), "admission busy") {
		t.Fatal("saturation misreported as stack state", w.Code, w.Body.String())
	}
	connection.Close()
	rows, e := s.Requests(ctx, tr.ID)
	if e != nil || len(rows) != 0 {
		t.Fatal("saturation queued work", e)
	}
	for len(s.incomingWaiters) > 0 {
		<-s.incomingWaiters
	}
	held = false
	accepted, e := s.Accept(ctx, tr.ID, ts, Sign([]byte(key), ts, body), body, &testDispatcher{})
	if e != nil {
		t.Fatal("same signed retry failed", e)
	}
	rows, e = s.Requests(ctx, tr.ID)
	if e != nil || len(rows) != 1 || rows[0].ID != accepted.ID {
		t.Fatal("retry did not admit exactly once", e)
	}
}
func TestReleaseBoundarySerialWaitCleanup(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	s.incomingAdmission <- struct{}{}
	held := true
	defer func() {
		if held {
			<-s.incomingAdmission
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	body := []byte(`{"id":"serial-wait"}`)
	ts := strconv.FormatInt(s.now().Unix(), 10)
	_, e := s.Accept(ctx, tr.ID, ts, Sign([]byte(key), ts, body), body, &testDispatcher{})
	if !errors.Is(e, errAdmissionSaturated) || len(s.incomingWaiters) != 0 {
		t.Fatal("serial wait leaked a burst permit", e)
	}
	rows, e := s.Requests(context.Background(), tr.ID)
	if e != nil || len(rows) != 0 {
		t.Fatal("timed out wait queued work", e)
	}
	<-s.incomingAdmission
	held = false
	if _, e = s.Accept(context.Background(), tr.ID, ts, Sign([]byte(key), ts, body), body, &testDispatcher{}); e != nil {
		t.Fatal("retry failed", e)
	}
}
func TestReleaseBoundaryValidationTimeoutIsNotPolicyDenial(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	_, e := acceptRequest(s, tr, key, `{"id":"timeout"}`, &testDispatcher{validate: func(Trigger) error { return context.DeadlineExceeded }})
	if !errors.Is(e, errStorage) || errors.Is(e, ErrUnsupported) {
		t.Fatal("timeout reported as policy denial", e)
	}
}
func TestReleaseBoundaryRotationDuringValidationIsRevoked(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{validate: func(Trigger) error { _, e := s.RotateTriggerSecret(context.Background(), tr.ID, tr.Version); return e }}
	if _, e := acceptRequest(s, tr, key, `{"id":"rotate-during-validation"}`, d); !errors.Is(e, ErrConflict) {
		t.Fatal("rotation did not invalidate authenticated snapshot", e)
	}
	rows, e := s.Requests(context.Background(), tr.ID)
	if e != nil || len(rows) != 0 {
		t.Fatal("revoked validation queued work", e)
	}
}
