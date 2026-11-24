package main

import (
	"bytes"
	"flag"
	"github.com/umayr/brine/internal/secretbindings"
	"os"
	"strings"
	"testing"
)

func TestQuarantineOperatorFlags(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) (string, error) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		var out bytes.Buffer
		act := registerQuarantineFlags(fs, &root, &out)
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		handled, err := act()
		if !handled {
			t.Fatal("action not handled")
		}
		return out.String(), err
	}
	if _, err := invoke("--process-quarantine=list"); err == nil {
		t.Fatal("missing state accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("read provisioned state", err)
	}
	q, err := secretbindings.NewQuarantine(root)
	if err != nil {
		t.Fatal(err)
	}
	id := secretbindings.DeploymentID("synthetic")
	token, err := q.Begin(id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := invoke("--process-quarantine=list")
	if err != nil || !strings.Contains(out, token) {
		t.Fatalf("list: %q %v", out, err)
	}
	for _, args := range [][]string{
		{"--process-quarantine=recover"},
		{"--process-quarantine=recover", "--quarantine-stack=synthetic", "--quarantine-token=" + token, "--quarantine-inspected", "--quarantine-same-host"},
	} {
		if _, err := invoke(args...); err == nil {
			t.Fatal("unsafe recovery accepted")
		}
		if q.Check(id) == nil {
			t.Fatal("intent cleared")
		}
	}
}
