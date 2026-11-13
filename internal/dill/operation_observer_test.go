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
	terminal *bool
	fail     bool
}

func (w observerWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	if !*w.terminal {
		w.t.Fatal("HTTP write preceded terminal persistence")
	}
	if w.fail {
		return 0, errors.New("delivery lost")
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
			started, finished := false, false
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
				return nil
			}}
			value, e := r.operation(WithOperationObserver(context.Background(), observer), "/synthetic/config.pkl", "plan", observerWriter{t: t, terminal: &finished, fail: mode == "delivery-fault"})
			if !finished {
				t.Fatal("no typed terminal")
			}
			if mode == "success" && e != nil {
				t.Fatal(e)
			}
			if mode == "finish-fault" {
				var he *OperationHistoryError
				if !errors.As(e, &he) || he.Invocation.Receipt == nil || he.Invocation.Receipt.Outcome != "succeeded" {
					t.Fatal("known receipt lost", e)
				}
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
func TestOperationObserverStartFailureRunsNoChild(t *testing.T) {
	r, log := consumerScript(t, consumerDiscovery(false), nil, 0, "")
	observer := testObserver{start: func() error { return errors.New("intent unavailable") }, finish: func(v OperationInvocation) error {
		if v.Receipt != nil || v.Status != "unknown" {
			t.Fatal("invented terminal")
		}
		return nil
	}}
	var out bytes.Buffer
	_, e := r.operation(WithOperationObserver(context.Background(), observer), "/synthetic/config.pkl", "plan", &out)
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
