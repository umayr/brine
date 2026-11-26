package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/webhooks"
)

// Opt-in pinned-pair test. Real released Dill/Pkl plan against a strict synthetic
// Docker API on a private disposable Unix socket; mutations are forbidden.
func TestWebhookReleasedDillPlan(t *testing.T) {
	binary, pkl, schema := os.Getenv("BRINE_TEST_DILL"), os.Getenv("BRINE_TEST_PKL"), os.Getenv("BRINE_TEST_DILL_SCHEMA")
	if binary == "" || pkl == "" || schema == "" {
		t.Skip("set exact released BRINE_TEST_DILL, BRINE_TEST_PKL and BRINE_TEST_DILL_SCHEMA")
	}
	if out, e := exec.Command(binary, "version").CombinedOutput(); e != nil || strings.TrimSpace(string(out)) != "v0.11.0" {
		t.Fatal("requires released Dill v0.11.0")
	}
	if out, e := exec.Command(pkl, "--version").CombinedOutput(); e != nil || !strings.HasPrefix(string(out), "Pkl 0.31.1 ") {
		t.Fatal("requires bundled Pkl0.31.1")
	}
	s, st, runner, _ := dispatcherServer(t)
	runner.Binary = binary
	runner.PklBinary = pkl
	dir, e := os.MkdirTemp("", "wh-engine-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "engine.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	runner.Socket = socket
	var mu sync.Mutex
	paths := []string{}
	engine := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method != "GET" && r.Method != "HEAD" {
			t.Errorf("plan attempted engine mutation: %s %s", r.Method, r.URL.Path)
			http.Error(w, "mutation forbidden", 405)
			return
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1.") {
			if i := strings.Index(path[1:], "/"); i >= 0 {
				path = path[i+1:]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/_ping":
			w.Header().Set("API-Version", "1.47")
			io.WriteString(w, "OK")
		case path == "/version":
			io.WriteString(w, `{"ApiVersion":"1.47","MinAPIVersion":"1.24"}`)
		case path == "/info":
			io.WriteString(w, `{"ID":"synthetic-webhook-engine"}`)
		case path == "/containers/json" || path == "/images/json" || path == "/networks" || path == "/volumes":
			io.WriteString(w, "[]")
		case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"No such container"}`)
		case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"No such image"}`)
		default:
			t.Errorf("unexpected synthetic engine observation: %s", r.URL.Path)
			http.Error(w, "unexpected observation", 500)
		}
	})}
	go engine.Serve(listener)
	defer engine.Close()
	content := fmt.Sprintf("amends %q\nconfig { name = \"example\"; engine = \"docker\"; services { [\"web\"] { image = \"nginx:alpine\" } } }\n", "file://"+filepath.ToSlash(schema))
	stack, e := st.Update("example", content)
	if e != nil {
		t.Fatal(e)
	}
	d := s.webhookDispatcher.(*webhookDispatcher)
	s.configureWebhookProducers()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.webhooks.RunTriggers(ctx, d)
	tr, key, e := s.webhooks.SaveTrigger(ctx, webhooks.Trigger{Name: "released plan", StackID: webhookStackID(stack), TargetID: d.localTargetID(), Action: "plan", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	body := `{"id":"released-plan-one"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	r := httptest.NewRequest("POST", "/hooks/"+tr.ID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Brine-Timestamp", ts)
	r.Header.Set("Brine-Signature", webhooks.Sign([]byte(key), ts, []byte(body)))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("request not accepted: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		records, e := s.webhooks.Requests(ctx, tr.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(records) > 0 && records[0].Status != "accepted" && records[0].Status != "running" {
			if records[0].Status != "completed" || records[0].Outcome != "success" {
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("released execution not confirmed: %+v; engine calls %v", records[0], paths)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released plan timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("released plan did not observe the pinned synthetic engine")
	}
	if events := journalEvents(t, s); len(events) != 1 || events[0].Outcome != "success" || events[0].Target.ID != tr.TargetID {
		t.Fatal("released request journal lost pinned identity", events)
	}
}
