package store

import (
	"strings"
	"testing"
)

func TestUpdateIntentRetainsOptionalQualifiedPredecessor(t *testing.T) {
	i := ManualDeploymentIntent{Host: "target", Stack: "demo", Target: strings.Repeat("a", 64), Mode: "update", Request: strings.Repeat("b", 32), PriorRequest: strings.Repeat("c", 32), Journal: strings.Repeat("d", 32), Selection: "sha256:" + strings.Repeat("e", 64), Stage: strings.Repeat("f", 32), Manifest: strings.Repeat("1", 64)}
	s, e := NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveManualIntent(i); e != nil {
		t.Fatal("qualified update preview blocked manual recovery", e)
	}
	got, e := s.GetManualIntent(i.Request)
	if e != nil || got != i {
		t.Fatal("predecessor not preserved", got, e)
	}
	for _, bad := range []string{i.Request, "malformed"} {
		next := i
		next.PriorRequest = bad
		if next.valid() {
			t.Fatal("invalid prior admitted", bad)
		}
	}
	i.Mode = "create"
	if i.valid() {
		t.Fatal("creation accepted update predecessor")
	}
}
