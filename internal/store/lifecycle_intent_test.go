package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestLifecycleRecoveryRetentionNeverEvictsUnresolvedOrManual(t *testing.T) {
	s, e := NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	base := ManualDeploymentIntent{Host: "target", Target: strings.Repeat("a", 64), Stack: "unknown", Mode: "stop", Request: strings.Repeat("b", 32), PriorRequest: strings.Repeat("c", 32), Journal: strings.Repeat("d", 32), Selection: "sha256:" + strings.Repeat("e", 64)}
	if e = s.SaveManualIntent(base); e != nil {
		t.Fatal(e)
	}
	manual := base
	manual.Mode = "remove"
	manual.Stack = "manual"
	manual.Request = strings.Repeat("a", 32)
	if e = s.SaveManualIntent(manual); e != nil {
		t.Fatal(e)
	}
	if e = s.ResolveManualIntent(manual); e != nil {
		t.Fatal(e)
	}
	for n := 1; n <= 140; n++ {
		i := base
		i.Stack = "automatic"
		i.Request = fmt.Sprintf("%032x", n)
		if e = s.SaveManualIntent(i); e != nil {
			t.Fatal(n, e)
		}
		if e = s.ResolveManualIntent(i); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.ListManualIntents()
	if e != nil || len(rows) > 131 {
		t.Fatal(len(rows), e)
	}
	if got, e := s.GetManualIntent(base.Request); e != nil || got.Resolved {
		t.Fatal("lost unresolved")
	}
	if got, e := s.GetManualIntent(manual.Request); e != nil || !got.Resolved {
		t.Fatal("lost manual evidence")
	}
	bad := base
	bad.Request = strings.Repeat("f", 32)
	if s.SaveManualIntent(bad) == nil {
		t.Fatal("unresolved lifecycle did not block")
	}
	for _, mode := range []string{"start", "stop", "restart"} {
		i := base
		i.Mode = mode
		if !i.valid() {
			t.Fatal("valid lifecycle denied", mode)
		}
		i.Stage = strings.Repeat("1", 32)
		if i.valid() {
			t.Fatal("lifecycle source override accepted")
		}
	}
}
