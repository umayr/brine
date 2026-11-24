package secretbindings

import (
	"os"
	"strings"
	"testing"
)

func TestQuarantineRecoveryRequiresBootChangeInspectionAndCAS(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	token, err := q.Begin("synthetic")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.RecoverAfterReboot("synthetic", token, true); err == nil {
		t.Fatal("Brine restart cleared same-boot intent")
	}
	// Simulate the platform boot provider changing. No real reboot is performed.
	reopened.boot = strings.Repeat("0", 32)
	if reopened.boot == token[:32] {
		reopened.boot = strings.Repeat("1", 32)
	}
	for _, tc := range []struct {
		name, token string
		inspected   bool
	}{
		{"synthetic", token, false}, {"synthetic", "", true}, {"wrong", token, true},
		{"synthetic", token[:32] + strings.Repeat("x", 32), true},
	} {
		if err = reopened.RecoverAfterReboot(tc.name, tc.token, tc.inspected); err == nil {
			t.Fatal("invalid recovery accepted")
		}
		if err = reopened.Check("synthetic"); err == nil {
			t.Fatal("denial lost intent")
		}
	}
	if err = reopened.RecoverAfterReboot("synthetic", token, true); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Check("synthetic"); err != nil {
		t.Fatal(err)
	}
	next, err := reopened.Begin("synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if next == token {
		t.Fatal("token reused")
	}
	if err = reopened.RecoverAfterReboot("synthetic", token, true); err == nil {
		t.Fatal("stale token cleared successor")
	}
	if err = reopened.Check("synthetic"); err == nil {
		t.Fatal("successor lost")
	}
}
