//go:build darwin || linux

package remotereveal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/revealaudit"
)

func TestOpenConfiguredRequiresProvisionedKeyAndEstablishedAudit(t *testing.T) {
	reg, _, key := fixture(t)
	base := t.TempDir()
	root := filepath.Join(base, "audit")
	events := []string{}
	signer := testSigner{key: key, events: &events}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfigured(context.Background(), reg, root, signer, remoteagent.Config{}); err == nil {
		t.Fatal("uninitialized audit accepted")
	}
	if _, err := revealaudit.Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfigured(context.Background(), reg, root, signer, remoteagent.Config{}); err != nil {
		t.Fatal("established audit rejected", err)
	}
	if err := os.WriteFile(filepath.Join(root, "head"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfigured(context.Background(), reg, root, signer, remoteagent.Config{}); err == nil {
		t.Fatal("damaged audit accepted")
	}
}
