package dill

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type testObserver struct {
	start  func() error
	finish func(OperationInvocation) error
}

func (o testObserver) Started() error                       { return o.start() }
func (o testObserver) Terminal(v OperationInvocation) error { return o.finish(v) }

type observerWriter struct {
	t        *testing.T
	terminal *bool // successful persistence, not merely callback entry
	calls    *int
	output   *bytes.Buffer
	fail     bool
}

func (w observerWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	*w.calls++
	if !*w.terminal {
		w.t.Error("HTTP write occurred without successful terminal persistence")
	}
	if w.fail {
		return 0, errors.New("delivery lost")
	}
	if w.output != nil {
		return w.output.Write(p)
	}
	return len(p), nil
}
func TestOperationObserverBeforeDiscoveryAndBeforeHTTP(t *testing.T) {
	for _, mode := range []string{"success", "finish-fault", "delivery-fault", "missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			data := append(consumerJSON(t, consumerReceipt("plan")), '\n')
			if mode == "missing" {
				data = nil
			}
			if mode == "malformed" {
				data = []byte("PRIVATE_SOURCE_STDERR\n")
			}
			r, log := consumerScript(t, consumerDiscovery(false), data, 0, "")
			started, finished, persisted := false, false, false
			writerCalls := 0
			var observed OperationInvocation
			observer := testObserver{start: func() error {
				if _, e := os.Stat(log); !os.IsNotExist(e) {
					t.Fatal("child before persisted start")
				}
				started = true
				return nil
			}, finish: func(v OperationInvocation) error {
				if !started {
					t.Fatal("finish before start")
				}
				finished = true
				observed = v
				if mode == "finish-fault" {
					return errors.New("disk full")
				}
				persisted = true
				return nil
			}}
			value, e := r.operation(WithOperationObserver(context.Background(), observer), "/synthetic/config.pkl", "plan", observerWriter{t: t, terminal: &persisted, calls: &writerCalls, fail: mode == "delivery-fault"})
			if !finished {
				t.Fatal("no typed terminal")
			}
			if mode == "success" && e != nil {
				t.Fatal(e)
			}
			if mode == "finish-fault" {
				var he *OperationHistoryError
				if !errors.As(e, &he) || he.Invocation.Receipt == nil || he.Invocation.Receipt.Outcome != "succeeded" || he.Invocation.Status != "succeeded" || he.Invocation.ExitCode != 0 {
					t.Fatal("known receipt lost", e)
				}
				if writerCalls != 0 {
					t.Errorf("failed terminal persistence wrote %d markers; want zero", writerCalls)
				}
			} else if writerCalls != 1 {
				t.Errorf("persisted terminal writer calls=%d; want exactly one", writerCalls)
			}
			if mode == "delivery-fault" {
				var delivery *OperationDeliveryError
				if !errors.As(e, &delivery) || value.Status != "succeeded" || observed.Status != "succeeded" {
					t.Fatal("delivery changed engine axes", e)
				}
			}
			if (mode == "missing" || mode == "malformed") && (observed.Receipt != nil || observed.Status != "unknown") {
				t.Fatal("invalid receipt invented")
			}
		})
	}
}
func TestOperationReadOnlyWithoutObserverPreservesMarker(t *testing.T) {
	r, log := consumerScript(t, consumerDiscovery(false), append(consumerJSON(t, consumerReceipt("plan")), '\n'), 0, "")
	r.RequireOperationObserver = true
	var out bytes.Buffer
	invocation, e := r.operation(context.Background(), "/synthetic/config.pkl", "plan", &out)
	if e != nil || invocation.Status != "succeeded" || strings.Count(out.String(), OperationMarker) != 1 {
		t.Fatal("read-only no-observer behavior changed", e, out.String())
	}
	calls, e := os.ReadFile(log)
	if e != nil || strings.Count(string(calls), "\n") != 2 {
		t.Fatal("positive child discovery/action missing", e)
	}
}

func TestOperationRequiredObserverMissingWritesNoMarkerOrChild(t *testing.T) {
	r, log := consumerScript(t, consumerDiscovery(true), nil, 0, "")
	r.RequireOperationObserver = true
	persisted, writerCalls := false, 0
	invocation, e := r.operation(context.Background(), "/synthetic/config.pkl", "stop", observerWriter{t: t, terminal: &persisted, calls: &writerCalls})
	var history *OperationHistoryError
	if !errors.As(e, &history) || invocation.Receipt != nil || invocation.Status != "unknown" || invocation.ExitCode != -1 {
		t.Fatal("missing observer did not fail closed", e)
	}
	if writerCalls != 0 {
		t.Fatalf("missing required observer wrote %d markers", writerCalls)
	}
	if _, e = os.Stat(log); !os.IsNotExist(e) {
		t.Fatal("child launched without required durable observer")
	}
}

func TestOperationObserverStartFailureRunsNoChild(t *testing.T) {
	r, log := consumerScript(t, consumerDiscovery(false), nil, 0, "")
	observer := testObserver{start: func() error { return errors.New("intent unavailable") }, finish: func(v OperationInvocation) error {
		if v.Receipt != nil || v.Status != "unknown" {
			t.Fatal("invented terminal")
		}
		return nil
	}}
	var out bytes.Buffer
	persisted, writerCalls := false, 0
	_, e := r.operation(WithOperationObserver(context.Background(), observer), "/synthetic/config.pkl", "plan", observerWriter{t: t, terminal: &persisted, calls: &writerCalls, output: &out})
	if writerCalls != 0 {
		t.Errorf("failed Started persistence wrote %d markers; want zero", writerCalls)
	}
	var he *OperationHistoryError
	if !errors.As(e, &he) {
		t.Fatal("history error hidden", e)
	}
	if _, e = os.Stat(log); !os.IsNotExist(e) {
		t.Fatal("subprocess launched")
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("diagnostic leaked")
	}
}
