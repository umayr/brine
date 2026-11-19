package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/config"
)

func TestOperationStateDirOptionRemainsDisabled(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"explicit empty", ""},
		{"nonempty", filepath.Join(t.TempDir(), "history")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldFlags, oldArgs := flag.CommandLine, os.Args
			t.Cleanup(func() { flag.CommandLine, os.Args = oldFlags, oldArgs })
			flag.CommandLine = flag.NewFlagSet("brine", flag.ContinueOnError)
			// An invalid bind prevents engine initialization even if the guard disappears.
			os.Args = []string{"brine", "--bind=invalid", "--operation-state-dir=" + tc.value}
			const want = "Alpha and strict secret options are disabled"
			if err := run(); err == nil || err.Error() != want {
				t.Fatalf("run() = %v, want %q", err, want)
			}
		})
	}
}

func TestGeneratedCredentialsNotLogged(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	// Persistent credentials reject writable/symlinked temp ancestry; use a
	// canonical, private service-HOME fixture just like real default state.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	dataDir, err := os.MkdirTemp(home, ".brine-startup-credentials-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.AuthPassword = ""
	cfg.APIToken = ""
	if _, err := buildAuth(cfg); err != nil {
		t.Fatalf("startup credential fixture requires canonical ACL-free HOME ancestry (CI: scripts/ci-test-go.sh): %v", err)
	}
	raw, err := os.ReadFile(cfg.DataDir + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"password:", "api token:"} {
		if strings.Contains(output.String(), marker) {
			t.Fatal("credentials logged")
		}
	}
	var creds struct {
		Password string `json:"password"`
		APIToken string `json:"api_token"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{creds.Password, creds.APIToken} {
		if secret == "" || strings.Contains(output.String(), secret) {
			t.Fatal("missing or logged credential")
		}
	}
}
