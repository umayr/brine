package secretbroker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClosedManagerDoesNotPrepareNewValues(t *testing.T) {
	_, m, f, i, _ := reviewFixture(t)
	m.Close()
	_, err := m.Prepare(context.Background(), i, []string{"TOKEN"})
	wantError(t, err, ErrUnavailable)
	if f.calls != 0 {
		t.Fatal("acquisition after close")
	}
}
func TestMutableAncestorRefused(t *testing.T) {
	parent := filepath.Dir(statePath(t))
	child := filepath.Join(parent, "private")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0700) })
	wantError(t, Provision(filepath.Join(child, "state.sqlite")), ErrUnsafePath)
}
