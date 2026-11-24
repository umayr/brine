package dill

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/secretbindings"
	"os"
	"os/exec"
	"testing"
)

func TestTrackedQuarantineBeforeLaunchAndRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	r := New("unused")
	r.ConfigureQuarantine(q)
	id := secretbindings.DeploymentID("synthetic")
	cmd := exec.Command("unused", "-p", "synthetic")
	calls := 0
	err = r.runTrackedWith(context.Background(), cmd, func(context.Context, *exec.Cmd) error {
		calls++
		reopened, e := secretbindings.NewQuarantine(root)
		if e != nil {
			t.Fatal(e)
		}
		if reopened.Check(id) == nil {
			t.Fatal("launch before durable intent")
		}
		return ErrProcessUnknown
	})
	if !errors.Is(err, ErrProcessUnknown) || calls != 1 {
		t.Fatalf("result: %v, calls %d", err, calls)
	}
	reopened, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	restart := New("unused")
	restart.ConfigureQuarantine(reopened)
	err = restart.runTrackedWith(context.Background(), cmd, func(context.Context, *exec.Cmd) error { calls++; return nil })
	if err == nil || calls != 1 {
		t.Fatal("restarted runner replayed quarantined operation")
	}
	if reopened.Check(id) == nil {
		t.Fatal("lost quarantine")
	}
	other := exec.Command("unused", "-p", "unrelated")
	err = restart.runTrackedWith(context.Background(), other, func(context.Context, *exec.Cmd) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Check(secretbindings.DeploymentID("unrelated")); err != nil {
		t.Fatal(err)
	}
}
