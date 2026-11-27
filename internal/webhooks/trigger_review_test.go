package webhooks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRevocationDuringValidationPreventsDispatch(t *testing.T) {
	for _, mode := range []string{"disable", "delete", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			s := testService(t, 10)
			tr, key := testTrigger(t, s)
			d := &testDispatcher{}
			if _, e := acceptRequest(s, tr, key, `{"id":"revoke-during-validation"}`, d); e != nil {
				t.Fatal(e)
			}
			d.validate = func(Trigger) error {
				switch mode {
				case "disable":
					tr.Enabled = false
					_, _, e := s.SaveTrigger(context.Background(), tr)
					return e
				case "delete":
					return s.DeleteTrigger(context.Background(), tr.ID, tr.Version)
				case "expiry":
					now := s.now()
					s.now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
				}
				return nil
			}
			if _, e := s.executeOne(context.Background(), d); e != nil {
				t.Fatal(e)
			}
			if d.calls != 0 {
				t.Fatal("revoked grant executed")
			}
			rows, _ := s.Requests(context.Background(), tr.ID)
			if rows[0].Status != "cancelled" {
				t.Fatal("revocation not retained")
			}
		})
	}
}
func TestRebindingCannotReadPriorReceipts(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	d := &testDispatcher{}
	if _, e := acceptRequest(s, tr, key, `{"id":"release-shared"}`, d); e != nil {
		t.Fatal(e)
	}
	s.executeOne(ctx, d)
	tr.StackID = "different"
	tr, newKey, e := s.SaveTrigger(ctx, tr)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = acceptRequest(s, tr, newKey, `{"id":"release-shared"}`, d); !errors.Is(e, ErrConflict) {
		t.Fatal("new grant could read old receipt", e)
	}
}
func TestValidationWorkIsRateLimited(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	tr.RequestsPerMinute = 1
	tr, _, _ = s.SaveTrigger(context.Background(), tr)
	calls := 0
	d := &testDispatcher{validate: func(Trigger) error { calls++; return errors.New("sensitive admission failure") }}
	if _, e := acceptRequest(s, tr, key, `{"id":"first-denied"}`, d); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	if _, e := acceptRequest(s, tr, key, `{"id":"second-denied"}`, d); !errors.Is(e, ErrRateLimited) {
		t.Fatal("validation work not bounded", e)
	}
	if calls != 1 {
		t.Fatal("over-limit validation executed")
	}
}
