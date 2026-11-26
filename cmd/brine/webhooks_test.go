package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestInitWebhooksIsOfflineAndNeverOverwritesState(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	key := filepath.Join(root, "key")
	if e := os.WriteFile(key, bytes.Repeat([]byte{6}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(data, "webhooks")
	oldFlags, oldArgs := flag.CommandLine, os.Args
	t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
	invoke := func() error {
		flag.CommandLine = flag.NewFlagSet("brine", flag.ContinueOnError)
		os.Args = []string{"brine", "--init-webhooks", "--webhooks-enabled", "--data-dir", data, "--webhooks-dir", state, "--webhooks-key-file", key}
		return run()
	}
	if e := invoke(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(state, "webhooks.db")); e != nil {
		t.Fatal("state not initialized")
	}
	if _, e := os.Stat(filepath.Join(data, "credentials.json")); !os.IsNotExist(e) {
		t.Fatal("offline initialization entered server credential startup")
	}
	if e := invoke(); e == nil {
		t.Fatal("existing state overwritten")
	}
}
