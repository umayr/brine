//go:build darwin || linux

package remoteui

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/remoteagent"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestOpenRevealHandlerFailsClosed(t *testing.T) {
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	reg, err := targetregistry.Open(filepath.Join(base, "reg"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "audit")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(base, "key")
	if err = os.WriteFile(key, []byte(hex.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatal(err)
	}
	open := func(path string) (*RevealHandler, error) {
		return OpenRevealHandler(context.Background(), a, reg, root, path, remoteagent.Config{})
	}
	if _, err = open(key); err == nil {
		t.Fatal("uninitialized audit accepted")
	}
	if _, err = revealaudit.Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err = open(key); err != nil {
		t.Fatal(err)
	}
	if _, err = open(filepath.Join(base, "missing")); err == nil || strings.Contains(err.Error(), base) {
		t.Fatal("missing key accepted or error leaked")
	}
	if err = os.WriteFile(filepath.Join(root, "head"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = open(key); err == nil {
		t.Fatal("damaged audit accepted")
	}
}
