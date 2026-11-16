package configvars

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/umayr/brine/internal/store"
)

func fixture(t *testing.T) (*Store, *store.FSStore) {
	t.Helper()
	dir := t.TempDir()
	stacks, err := store.NewFSStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stacks.Create("app", "config {}"); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, stacks)
	if err != nil {
		t.Fatal(err)
	}
	return s, stacks
}
func TestEffectiveAndPersistence(t *testing.T) {
	s, stacks := fixture(t)
	for _, v := range []struct{ scope, key, value string }{{"", "DOMAIN", "shared"}, {"", "COMMON", "global"}, {"app", "DOMAIN", "local"}, {"app", "EMPTY", ""}} {
		if err := s.Set(v.scope, v.key, v.value, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Effective("app")
	if err != nil {
		t.Fatal(err)
	}
	if got["BRINE_VAR_DOMAIN"] != "local" || got["BRINE_VAR_COMMON"] != "global" || got["BRINE_VAR_EMPTY"] != "" {
		t.Fatalf("bad variables: %#v", got)
	}
	reopened, err := New(filepath.Dir(s.dir), stacks)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := reopened.Values("app")
	if err != nil || vals["DOMAIN"] != "local" {
		t.Fatalf("reload %v %v", vals, err)
	}
	if err := s.Set("app", "DOMAIN", "", true); err != nil {
		t.Fatal(err)
	}
	got, err = s.Effective("app")
	if err != nil || got["BRINE_VAR_DOMAIN"] != "shared" {
		t.Fatalf("override removal %v %v", got, err)
	}
	if _, err := stacks.Create("other", "config {}"); err != nil {
		t.Fatal(err)
	}
	other, err := s.Effective("other")
	if err != nil || other["BRINE_VAR_DOMAIN"] != "shared" {
		t.Fatalf("other stack: %v %v", other, err)
	}
	if _, ok := other["BRINE_VAR_EMPTY"]; ok {
		t.Fatal("stack-local value leaked")
	}
	if err := stacks.Delete("app"); err != nil {
		t.Fatal(err)
	}
	if _, err := stacks.Create("app", "config {}"); err != nil {
		t.Fatal(err)
	}
	vals, err = s.Values("app")
	if err != nil || len(vals) != 0 {
		t.Fatalf("recreated stack inherited values: %v %v", vals, err)
	}
}
func TestRejectInvalidAndCorrupt(t *testing.T) {
	s, _ := fixture(t)
	for _, name := range []string{"PATH=hack", "bad", "BRINE_VAR_BAD", "bad-name", ""} {
		if err := s.Set("", name, "x", false); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := s.Set("", "FOO", "\x00", false); err == nil {
		t.Fatal("accepted NUL")
	}
	if err := s.Set("", "FOO", strings.Repeat("x", 16385), false); err == nil {
		t.Fatal("accepted oversized value")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(s.dir, "variables.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Effective("app"); err == nil {
		t.Fatal("followed symlink")
	}
}
func TestConcurrentUpdates(t *testing.T) {
	s, _ := fixture(t)
	var wg sync.WaitGroup
	for _, name := range []string{"A", "B", "C", "D"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Set("", name, name, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := s.Values("")
	if err != nil || len(got) != 4 {
		t.Fatalf("got %v %v", got, err)
	}
}
