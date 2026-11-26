package webhooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInFlightRevocationCannotResurrectRetry(t *testing.T) {
	for _, change := range []string{"url", "secret", "delete"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			s := testService(t, 10)
			ep, originalKey := testEndpoint(t, s)
			s.Publish(ctx, testEvent("evt_revoke"))
			s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				switch change {
				case "url":
					ep.URL = "https://replacement.example.com/events"
					_, key, e := s.SaveEndpoint(ctx, ep)
					if e != nil || key == "" || key == originalKey {
						t.Error("URL transfer did not rotate key")
					}
				case "secret":
					if _, e := s.RotateSecret(ctx, ep.ID, ep.Version); e != nil {
						t.Error(e)
					}
				case "delete":
					if e := s.DeleteEndpoint(ctx, ep.ID, ep.Version); e != nil {
						t.Error(e)
					}
				}
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			if _, e := s.sendOne(ctx); e != nil {
				t.Fatal(e)
			}
			ds, _ := s.Deliveries(ctx, ep.ID)
			if ds[0].Status != "cancelled" {
				t.Fatalf("retired delivery resurrected: %+v", ds[0])
			}
			if sent, e := s.sendOne(ctx); sent || e != nil {
				t.Fatal("retired request retried")
			}
		})
	}
}
func TestScopeUsesStableIDOnly(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	ep.Stacks = []string{"subscribed-id"}
	s.SaveEndpoint(ctx, ep)
	event := testEvent("evt_collision")
	event.Stack.ID = "other-id"
	event.Stack.Name = "subscribed-id"
	if e := s.Publish(ctx, event); e != nil {
		t.Fatal(e)
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if len(ds) != 0 {
		t.Fatal("name collision escaped scope")
	}
}
func TestCrashReservationsBoundRetries(t *testing.T) {
	ctx := context.Background()
	s := testService(t, 10)
	ep, _ := testEndpoint(t, s)
	s.Publish(ctx, testEvent("evt_crash"))
	for n := 1; n <= 8; n++ {
		s.db.Exec("UPDATE deliveries SET next_at=0")
		c, e := s.claim(ctx)
		if e != nil || c.Attempts != n {
			t.Fatalf("reservation %d: %+v %v", n, c, e)
		}
		if e = s.recoverClaims(ctx); e != nil {
			t.Fatal(e)
		}
	}
	ds, _ := s.Deliveries(ctx, ep.ID)
	if ds[0].Status != "exhausted" || ds[0].Attempts != 8 {
		t.Fatal("crashes escaped budget")
	}
	as, _ := s.Attempts(ctx, ds[0].ID)
	if len(as) != 8 {
		t.Fatal("abandoned attempts not recorded")
	}
}
func TestMissingVerifierFailsClosed(t *testing.T) {
	s := testService(t, 10)
	testEndpoint(t, s)
	s.db.Exec("DELETE FROM metadata WHERE key='key-check'")
	cfg := s.cfg
	cfg.EncryptionKey = make([]byte, 32)
	s.Close()
	if reopened, e := Open(cfg); e == nil {
		reopened.Close()
		t.Fatal("populated store accepted missing verifier")
	}
}
func TestWorkerCanRestartAfterCancellation(t *testing.T) {
	s := testService(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.Run(ctx); e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := s.Run(ctx); e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal("worker cannot restart", e)
	}
}
