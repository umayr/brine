package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentLoginOverHTTP(t *testing.T) {
	a := newTestAuth(t, Config{})
	server := httptest.NewServer(http.HandlerFunc(a.HandleLogin))
	defer server.Close()
	var admitted, limited, busy, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := server.Client().PostForm(server.URL+"/login", url.Values{"user": {"admin"}, "password": {"wrong"}})
			if err != nil {
				other.Add(1)
				return
			}
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			switch r.StatusCode {
			case http.StatusUnauthorized:
				admitted.Add(1)
			case http.StatusTooManyRequests:
				limited.Add(1)
			case http.StatusServiceUnavailable:
				busy.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if other.Load() != 0 || admitted.Load() > maxAttempts {
		t.Fatalf("admitted=%d limited=%d busy=%d other=%d", admitted.Load(), limited.Load(), busy.Load(), other.Load())
	}
	// Busy responses do not use a slot. Sequential retries must exhaust the remainder.
	for admitted.Load() < maxAttempts {
		r, err := server.Client().PostForm(server.URL+"/login", url.Values{"user": {"admin"}, "password": {"wrong"}})
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("retry status %d", r.StatusCode)
		}
		admitted.Add(1)
	}
	r, err := server.Client().PostForm(server.URL+"/login", url.Values{"user": {"admin"}, "password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after budget: %d", r.StatusCode)
	}
}
