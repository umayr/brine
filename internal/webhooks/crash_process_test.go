package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This is a queue-component process-crash check. The dispatcher's only effect
// is a synced marker; it does not claim real target execution or browser auth.
type crashMarkerDispatcher struct {
	marker string
	calls  atomic.Int32
	probes atomic.Int32
	block  bool
}

func (d *crashMarkerDispatcher) Validate(context.Context, Trigger) error { return nil }
func (d *crashMarkerDispatcher) Execute(ctx context.Context, trigger Trigger, r IncomingRequest) TriggerResult {
	if trigger.StackID == "probe-stack" {
		d.probes.Add(1)
		return TriggerResult{Status: "completed", Outcome: "success", OperationID: r.ID}
	}
	d.calls.Add(1)
	f, e := os.OpenFile(d.marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		panic(e)
	}
	if _, e = f.WriteString("entered\n"); e != nil {
		panic(e)
	}
	if e = f.Sync(); e != nil {
		panic(e)
	}
	if e = f.Close(); e != nil {
		panic(e)
	}
	if d.block {
		if e = os.WriteFile(d.marker+".ready", []byte("synced"), 0600); e != nil {
			panic(e)
		}
		<-ctx.Done()
	}
	return TriggerResult{Status: "unknown", Outcome: "unknown", OperationID: r.ID}
}
func TestIncomingWorkerHardCrashDoesNotReplay(t *testing.T) {
	if root := os.Getenv("BRINE_TEST_QUEUE_CRASH_ROOT"); root != "" {
		s, e := OpenExisting(Config{Directory: filepath.Join(root, "state"), EncryptionKey: bytes.Repeat([]byte{1}, 32)})
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		d := &crashMarkerDispatcher{marker: filepath.Join(root, "entered"), block: true}
		if e = s.RunTriggers(ctx, d); e != nil {
			t.Fatal(e)
		}
		return
	}
	root := t.TempDir()
	cfg := Config{Directory: filepath.Join(root, "state"), EncryptionKey: bytes.Repeat([]byte{1}, 32)}
	s, e := Open(cfg)
	if e != nil {
		t.Fatal(e)
	}
	tr, key := testTrigger(t, s)
	probe, probeKey, e := s.SaveTrigger(context.Background(), Trigger{Name: "worker startup probe", Enabled: true, StackID: "probe-stack", TargetID: "probe-target", Action: "plan"})
	if e != nil {
		t.Fatal(e)
	}
	request, e := acceptRequest(s, tr, key, `{"id":"crash-once"}`, &testDispatcher{})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestIncomingWorkerHardCrashDoesNotReplay$", "-test.timeout=40s")
	child.Env = append(os.Environ(), "BRINE_TEST_QUEUE_CRASH_ROOT="+root)
	var output bytes.Buffer
	child.Stdout = &output
	child.Stderr = &output
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	})
	entered := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		b, e := os.ReadFile(filepath.Join(root, "entered.ready"))
		if e == nil && string(b) == "synced" {
			entered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !entered {
		child.Process.Kill()
		child.Wait()
		t.Fatal("child did not enter durable running dispatch", output.String())
	}
	db, e := sql.Open("sqlite", filepath.Join(cfg.Directory, "webhooks.db"))
	if e != nil {
		t.Fatal(e)
	}
	var state string
	e = db.QueryRow("SELECT status FROM incoming_requests WHERE id=?", request.ID).Scan(&state)
	db.Close()
	if e != nil || state != "running" {
		t.Fatal("kill boundary was not durable running", state, e)
	}
	if e = child.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e = child.Wait(); e == nil {
		t.Fatal("worker exited normally rather than being hard-killed")
	}
	for boot := 0; boot < 2; boot++ {
		func() {
			reopened, e := OpenExisting(cfg)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			d := &crashMarkerDispatcher{marker: filepath.Join(root, "entered")}
			defer func() {
				cancel()
				select {
				case e := <-done:
					if e != nil {
						t.Error(e)
					}
				case <-time.After(5 * time.Second):
					t.Error("worker shutdown deadline exceeded")
				}
				if e := reopened.Close(); e != nil {
					t.Error(e)
				}
			}()
			probeRequest, e := acceptRequest(reopened, probe, probeKey, `{"id":"probe-`+strconv.Itoa(boot)+`"}`, d)
			if e != nil {
				t.Fatal(e)
			}
			go func() { done <- reopened.RunTriggers(ctx, d) }()
			passed := false
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
				rows, e := reopened.Requests(ctx, probe.ID)
				if e != nil {
					t.Fatal(e)
				}
				for _, r := range rows {
					if r.ID == probeRequest.ID && r.Status == "completed" && d.probes.Load() == 1 {
						passed = true
					}
				}
				if passed {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !passed {
				t.Fatal("reopened worker did not complete fresh independent probe")
			}
			recovered := false
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
				rows, e := reopened.Requests(ctx, tr.ID)
				if e != nil {
					t.Fatal(e)
				}
				if len(rows) == 1 && rows[0].ID == request.ID && rows[0].Status == "unknown" && rows[0].Outcome == "unknown" && rows[0].Reason == "restart_requires_inspection" {
					recovered = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !recovered {
				t.Fatal("crashed request did not recover unknown")
			}
			duplicate, e := acceptRequest(reopened, tr, key, `{"id":"crash-once"}`, d)
			if e != nil || duplicate.ID != request.ID || duplicate.Status != "unknown" {
				t.Fatal("crash duplicate changed identity or queued replay", e)
			}
			if _, e = acceptRequest(reopened, tr, key, `{"id":"different-after-crash"}`, d); !errors.Is(e, ErrBusy) {
				t.Fatal("crash failed to hold binding", e)
			}
			if d.calls.Load() != 0 {
				t.Fatal("restart dispatched crashed operation")
			}
		}()
	}

	b, e := os.ReadFile(filepath.Join(root, "entered"))
	if e != nil || strings.Count(string(b), "entered\n") != 1 {
		t.Fatal("dispatcher repeated after crash", e)
	}
	t.Log("hard-killed running worker reopened twice: one marker, stable unknown and blocked binding, no replay")
}
