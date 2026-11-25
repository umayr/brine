package remoteui

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
)

type restartReaderFunc func(context.Context, snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error)

func (f restartReaderFunc) DiscoverRestartMetadata(ctx context.Context, q snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error) {
	return f(ctx, q)
}

func restartHTTPFixture(t *testing.T) (*Deployment, *http.Cookie) {
	t.Helper()
	a, err := auth.New(auth.Config{User: "admin", Password: "generated-password"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=generated-password"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.HandleLogin(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pairing := `{"version":1,"certificateSha256":"` + strings.Repeat("a", 64) + `","token":"` + strings.Repeat("A", 43) + `","agentPublicKey":"` + strings.Repeat("b", 64) + `","engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`
	row := targetregistry.Record{ID: "demo", Revision: 1, HTTPSAddress: "127.0.0.1:1", HTTPSPairing: targetregistry.PairingSecret(pairing)}
	if err := reg.AddContext(context.Background(), row, row); err != nil {
		t.Fatal(err)
	}
	return &Deployment{auth: a, registry: reg}, cookies[0]
}

func TestRestartMetadataHTTP(t *testing.T) {
	for _, mode := range []string{"success", "denied", "no-policy", "revoked-after-read", "oversize", "forgotten", "no-cookie", "bearer"} {
		t.Run(mode, func(t *testing.T) {
			h, cookie := restartHTTPFixture(t)
			var reads, checks atomic.Int32
			var revoked atomic.Bool
			var expectedScope string
			_, enrollment, lease, err := h.registry.AcquireHistoryRead(context.Background(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			expectedScope = hex.EncodeToString(enrollment[:])
			if err = lease.Close(); err != nil {
				t.Fatal(err)
			}
			policy := restartMetadataAccess(func(ctx context.Context, actor archiveReadActor, row targetregistry.Record, id targetregistry.HistoryIncarnation) error {
				checks.Add(1)
				if actor.operator == "" || actor.session == "" || row.ID != "demo" || id != enrollment || mode == "denied" || revoked.Load() {
					return errRestartMetadata
				}
				return nil
			})
			if mode == "no-policy" {
				policy = nil
			}
			reader := restartReaderFunc(func(ctx context.Context, q snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error) {
				reads.Add(1)
				if string(q.ApprovedTargetScope) != expectedScope || q.Limit != 32 || q.After != "cursor" {
					return snapshothistory.RestartDiscoveryPage{}, errRestartMetadata
				}
				if err := q.Authorizer.AuthorizeSnapshotHistoryRestart(ctx, q.ApprovedTargetScope); err != nil {
					return snapshothistory.RestartDiscoveryPage{}, err
				}
				page := snapshothistory.RestartDiscoveryPage{Records: []snapshothistory.RestartMetadata{{Locator: snapshothistory.ObjectID{1}, AttemptID: []byte("intent-only")}}, More: true, Next: "next"}
				if mode == "revoked-after-read" {
					revoked.Store(true)
				}
				if mode == "oversize" {
					page.Records[0].AttemptID = make([]byte, 129)
				}
				return page, nil
			})
			if mode == "forgotten" {
				if err := h.registry.ForgetHTTPS(context.Background(), "demo"); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(h.protectedRestartMetadata(reader, policy))
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			req, _ := http.NewRequest("GET", server.URL+"/?host=demo&after=cursor", nil)
			if mode != "no-cookie" {
				req.AddCookie(cookie)
			}
			if mode == "bearer" {
				req.Header.Set("Authorization", "Bearer generated-marker")
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("cacheable response")
			}
			if mode == "success" {
				if res.StatusCode != 200 || reads.Load() != 1 || checks.Load() < 3 {
					t.Fatalf("status=%d reads=%d checks=%d", res.StatusCode, reads.Load(), checks.Load())
				}
				var page snapshothistory.RestartDiscoveryPage
				if json.Unmarshal(body, &page) != nil || len(page.Records) != 1 || string(page.Records[0].AttemptID) != "intent-only" || !page.More || page.Next != "next" {
					t.Fatal("lost metadata or pagination")
				}
			} else {
				if res.StatusCode == 200 || strings.Contains(string(body), "intent-only") {
					t.Fatal("denial leaked metadata")
				}
				wantReads := int32(0)
				if mode == "revoked-after-read" || mode == "oversize" {
					wantReads = 1
				}
				if reads.Load() != wantReads {
					t.Fatalf("reads=%d want=%d", reads.Load(), wantReads)
				}
			}
		})
	}
}

func TestRestartMetadataLogoutCancelsRead(t *testing.T) {
	h, cookie := restartHTTPFixture(t)
	entered, returned := make(chan struct{}), make(chan struct{})
	reader := restartReaderFunc(func(ctx context.Context, _ snapshothistory.RestartDiscoveryRequest) (snapshothistory.RestartDiscoveryPage, error) {
		close(entered)
		<-ctx.Done()
		close(returned)
		return snapshothistory.RestartDiscoveryPage{Next: "must-not-deliver"}, nil
	})
	policy := restartMetadataAccess(func(context.Context, archiveReadActor, targetregistry.Record, targetregistry.HistoryIncarnation) error {
		return nil
	})
	server := httptest.NewServer(h.protectedRestartMetadata(reader, policy))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	result := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest("GET", server.URL+"/?host=demo", nil)
		req.AddCookie(cookie)
		res, err := client.Do(req)
		if err != nil {
			result <- ""
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		result <- string(b)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reader not entered")
	}
	logout := httptest.NewRequest("POST", "/logout", nil)
	logout.AddCookie(cookie)
	h.auth.HandleLogout(httptest.NewRecorder(), logout)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("logout did not cancel read")
	}
	select {
	case body := <-result:
		if strings.Contains(body, "must-not-deliver") {
			t.Fatal("cancelled metadata delivered")
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
}
