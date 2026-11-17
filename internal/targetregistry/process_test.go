package targetregistry

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessCASHelper(t *testing.T) {
	dir := os.Getenv("BRINE_REGISTRY_CAS_HELPER")
	if dir == "" {
		return
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	next := old
	next.Revision++
	next.Host = os.Getenv("BRINE_REGISTRY_CAS_HOST")
	err = r.Update(old, next, ReviewedUpdate{old, next})
	if err == nil {
		return
	}
	if errors.Is(err, ErrConflict) {
		os.Exit(3)
	}
	t.Fatal(err)
}
func TestProcessCAS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "registry")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := record()
	if err = r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, 2)
	for i, host := range []string{"one.example", "two.example"} {
		c := exec.Command(os.Args[0], "-test.run=^TestProcessCASHelper$")
		c.Env = append(os.Environ(), "BRINE_REGISTRY_CAS_HELPER="+dir, "BRINE_REGISTRY_CAS_HOST="+host)
		cmds[i] = c
		if err = c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	ok, conflict := 0, 0
	for _, c := range cmds {
		err := c.Wait()
		if err == nil {
			ok++
		} else if e, yes := err.(*exec.ExitError); yes && e.ExitCode() == 3 {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
}
