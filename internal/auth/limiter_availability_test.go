package auth

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAttemptLimiterIndependentOfSessionMutex(t *testing.T) {
	a := newTestAuth(t, Config{})
	a.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- a.allowAttempt("peer") }()
	select {
	case ok := <-done:
		a.mu.Unlock()
		if !ok {
			t.Fatal("first attempt rejected")
		}
	case <-time.After(time.Second):
		a.mu.Unlock()
		<-done
		t.Fatal("attempt admission blocked on session mutex")
	}
}

func TestCapacityConcurrentChurnAndExpiry(t *testing.T) {
	a := newTestAuth(t, Config{})
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	for i := 0; i < maxAttemptIPs; i++ {
		if !a.allowAttempt(fmt.Sprint(i)) {
			t.Fatal("unexpected capacity rejection", i)
		}
	}
	var existing, newcomers atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				if a.allowAttempt("0") {
					existing.Add(1)
				}
			} else if a.allowAttempt(fmt.Sprintf("new-%d", i)) {
				newcomers.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if existing.Load() != maxAttempts-1 || newcomers.Load() != 0 {
		t.Fatalf("existing admitted=%d new admitted=%d", existing.Load(), newcomers.Load())
	}
	// Resetting an existing entry must move its heap node, not let it be
	// reclaimed ahead of still-expired entries when a new peer arrives.
	now = now.Add(attemptWindow)
	if !a.allowAttempt("0") || !a.allowAttempt("new") {
		t.Fatal("expiry did not reopen admission")
	}
	if a.attempts["0"] == nil || a.attempts["0"].count != 1 {
		t.Fatal("live reset entry was evicted")
	}
	if len(a.attempts) != maxAttemptIPs || len(a.expirations) != maxAttemptIPs {
		t.Fatal("capacity invariant violated")
	}
	for i, att := range a.expirations {
		if att.index != i || a.attempts[att.ip] != att || (i > 0 && att.reset.Before(a.expirations[(i-1)/2].reset)) {
			t.Fatalf("heap invariant violated at %d", i)
		}
	}
}

func TestFullCapacityOverConcurrentHTTP(t *testing.T) {
	a := newTestAuth(t, Config{})
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	for i := 0; i < maxAttemptIPs; i++ {
		if !a.allowAttempt(fmt.Sprintf("peer-%d", i)) {
			t.Fatal("fill failed", i)
		}
	}
	for i := 1; i < maxAttempts; i++ {
		if !a.allowAttempt("peer-0") {
			t.Fatal("fill budget failed")
		}
	}
	// Use real HTTP transport; this test-only adapter simulates many direct
	// peers because a loopback listener otherwise sees only one source address.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = r.URL.Query().Get("peer") + ":1234"
		a.HandleLogin(w, r)
	}))
	defer server.Close()
	var limited, busy, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			peer := fmt.Sprintf("unknown-%d", i)
			if i%2 == 0 {
				peer = "peer-0"
			}
			u := server.URL + "/login?peer=" + url.QueryEscape(peer)
			r, err := server.Client().PostForm(u, url.Values{"user": {"admin"}, "password": {"wrong"}})
			if err != nil {
				other.Add(1)
				return
			}
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			switch r.StatusCode {
			case http.StatusTooManyRequests:
				limited.Add(1)
			case http.StatusServiceUnavailable:
				busy.Add(1)
			default:
				other.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if other.Load() != 0 || limited.Load() == 0 {
		t.Fatalf("limited=%d busy=%d other=%d", limited.Load(), busy.Load(), other.Load())
	}
	for _, peer := range []string{"peer-0", "new-at-capacity"} {
		r, err := server.Client().PostForm(server.URL+"/login?peer="+url.QueryEscape(peer), url.Values{"user": {"admin"}, "password": {"wrong"}})
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("peer %s: got %d", peer, r.StatusCode)
		}
	}
	now = now.Add(attemptWindow)
	r, err := server.Client().PostForm(server.URL+"/login?peer=new", url.Values{"user": {"admin"}, "password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after expiry: %d", r.StatusCode)
	}
}
