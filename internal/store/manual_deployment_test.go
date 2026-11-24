package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualIntentDurableRemoteOnlyRemoval(t *testing.T) {
	root := t.TempDir()
	s, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	i := ManualDeploymentIntent{Host: "target", Target: strings.Repeat("a", 64), Stack: "absent", Mode: "remove", Request: strings.Repeat("b", 32), PriorRequest: strings.Repeat("c", 32), Journal: strings.Repeat("d", 32), Selection: "sha256:" + strings.Repeat("e", 64)}
	if err = s.SaveManualIntent(i); err != nil {
		t.Fatal(err)
	}
	again, _ := NewFSStore(root)
	got, err := again.GetManualIntent(i.Request)
	if err != nil || got != i {
		t.Fatal(got, err)
	}
	next := i
	next.Request = strings.Repeat("f", 32)
	if again.SaveManualIntent(next) == nil {
		t.Fatal("overlap permitted")
	}
	wrong := i
	wrong.Journal = strings.Repeat("0", 32)
	if again.ResolveManualIntent(wrong) == nil {
		t.Fatal("foreign binding resolved")
	}
	if err = again.ResolveManualIntent(i); err != nil {
		t.Fatal(err)
	}
	if err = again.SaveManualIntent(next); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, manualIntentFile)); err != nil {
		t.Fatal(err)
	}
	if _, err = again.ListManualIntents(); err == nil {
		t.Fatal("missing established ledger accepted")
	}
}
func TestManualIntentCorruptStateFailsClosed(t *testing.T) {
	for _, raw := range []string{"null", "null garbage", "{}", "[{\"unknown\":true}]"} {
		t.Run(raw, func(t *testing.T) {
			root := t.TempDir()
			s, _ := NewFSStore(root)
			if err := os.WriteFile(filepath.Join(root, manualIntentFile), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ListManualIntents(); err == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
}
