package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/revealaudit"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestRevealStartupFailsClosed(t *testing.T) {
	cfg := config.Default()
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = privateTestDir(t)
	cfg.RemoteForwardDir = privateTestDir(t)
	cfg.RemoteTimeout = time.Second
	if err := targetregistry.Init(cfg.RemoteRegistryDir); err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.OpenExisting(cfg.RemoteRegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func() error {
		s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
		err := s.prepareRemote(testLease{reg}, testObserver{})
		if s.remoteCancel != nil {
			s.remoteCancel()
		}
		return err
	}
	if err := prepare(); err != nil {
		t.Fatalf("read without reveal: %v", err)
	}
	cfg.RemoteRevealEnabled = true
	cfg.RemoteRevealAuditDir = privateTestDir(t)
	cfg.RemoteRevealSeedPath = filepath.Join(privateTestDir(t), "seed")
	if err := prepare(); err == nil {
		t.Fatal("missing seed and audit accepted")
	}
	if err := os.WriteFile(cfg.RemoteRevealSeedPath, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err == nil {
		t.Fatal("damaged seed accepted")
	}
	if err := os.WriteFile(cfg.RemoteRevealSeedPath, []byte("0000000000000000000000000000000000000000000000000000000000000000"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err == nil {
		t.Fatal("uninitialized audit accepted")
	}
	if _, err := revealaudit.Initialize(context.Background(), cfg.RemoteRevealAuditDir); err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err != nil {
		t.Fatalf("valid configured reveal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.RemoteRevealAuditDir, "head"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(); err == nil {
		t.Fatal("damaged audit accepted")
	}
}

func TestRevealConfigurationRequiresCompleteOptIn(t *testing.T) {
	c := config.Default()
	c.RemoteRevealSeedPath = "/seed"
	if c.Validate() == nil {
		t.Fatal("seed without opt-in accepted")
	}
	c.RemoteRevealEnabled = true
	if c.Validate() == nil {
		t.Fatal("partial reveal accepted")
	}
	c.RemoteEnabled = true
	c.RemoteRegistryDir = "/registry"
	c.RemoteForwardDir = "/forward"
	c.RemoteTimeout = time.Second
	c.RemoteRevealAuditDir = "/audit"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.NoAuth = true
	if c.Validate() == nil {
		t.Fatal("no-auth accepted")
	}
}
