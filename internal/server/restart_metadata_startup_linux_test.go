//go:build linux

package server

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/config"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/snapshothistory"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/sys/unix"
)

func metadataArchiveHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("HOME")
	if !regexp.MustCompile(`^/brine-go-test-home\.[A-Za-z0-9]{8}$`).MatchString(home) {
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			t.Fatal("archive tests require the CI-generated private HOME")
		}
		t.Skip("private archive fixture unavailable; run bash scripts/ci-test-go.sh on disposable Linux")
	}
	// Recheck the CI root and its single child before creating any fixture.
	for _, path := range []string{"/", home} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal("cannot open generated fixture ancestry")
		}
		var st unix.Stat_t
		statErr := unix.Fstat(fd, &st)
		_, access := unix.Fgetxattr(fd, "system.posix_acl_access", nil)
		_, inherited := unix.Fgetxattr(fd, "system.posix_acl_default", nil)
		_ = unix.Close(fd)
		if statErr != nil || !errors.Is(access, unix.ENODATA) || !errors.Is(inherited, unix.ENODATA) ||
			st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) ||
			(path == home && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700)) {
			t.Fatal("unsafe generated fixture ancestry")
		}
	}
	base, err := os.MkdirTemp(home, "attended-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	return base
}

func metadataStartupFixture(t *testing.T) (*Server, *config.Config) {
	t.Helper()
	root := metadataArchiveHome(t)
	for _, name := range []string{"archive", "keys", "workspace", "registry"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(root, "keys", "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := targetregistry.Init(filepath.Join(root, "registry")); err != nil {
		t.Fatal(err)
	}
	reg, err := targetregistry.OpenExisting(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	pairing := `{"version":1,"certificateSha256":"` + strings.Repeat("a", 64) + `","token":"` + strings.Repeat("A", 43) + `","agentPublicKey":"` + strings.Repeat("b", 64) + `","engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`
	row := targetregistry.Record{ID: "demo", Revision: 1, HTTPSAddress: "127.0.0.1:1", HTTPSPairing: targetregistry.PairingSecret(pairing)}
	if err := reg.AddContext(context.Background(), row, row); err != nil {
		t.Fatal(err)
	}
	_, id, lease, err := reg.AcquireHistoryRead(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = filepath.Join(root, "workspace")
	cfg.RemoteEnabled = true
	cfg.RemoteRegistryDir = filepath.Join(root, "registry")
	cfg.RemoteTimeout = time.Second
	cfg.RestartMetadata = config.RestartMetadataSettings{Enabled: true, ArchiveDir: filepath.Join(root, "archive"), KeyFile: key, StoreID: strings.Repeat("ab", 32), MaxBytes: 1 << 20, MaxOperations: 10, Enrollments: []string{"demo=" + hex.EncodeToString(id[:])}}
	a, err := auth.New(auth.Config{User: "admin", Password: "generated-password"})
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, agent.NewLocalAgent(nil, dill.New("dill")), nil, nil, nil, a)
	if err := s.prepareRemote(testLease{reg}, testObserver{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.remoteCancel)
	return s, cfg
}

func TestMetadataStartupExistingArchiveThroughServer(t *testing.T) {
	s, cfg := metadataStartupFixture(t)
	a := s.authn
	// Opening absent identity must not initialize the empty archive.
	endpoint, err := s.openRestartMetadata(context.Background(), cfg)
	if err == nil || endpoint.handler != nil {
		t.Fatal("opened uninitialized archive")
	}
	entries, err := os.ReadDir(cfg.RestartMetadata.ArchiveDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("startup modified uninitialized archive")
	}
	options, _, err := cfg.RestartMetadataStartup()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshothistory.Initialize(context.Background(), options); err != nil {
		t.Fatal(err)
	} // fixture provisioning only
	s.metadata = s.beginRestartMetadata(context.Background())
	defer s.metadata.stop(time.Second)
	waitMetadataReady(t, s.metadata)
	ts := httptest.NewServer(s.router)
	defer ts.Close()
	login := httptest.NewRequest("POST", "/login", strings.NewReader("user=admin&password=generated-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	a.HandleLogin(rr, login)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("login failed")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/remote/archive/metadata?host=demo", nil)
	req.AddCookie(cookies[0])
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), `"Records"`) {
		t.Fatalf("metadata response status %d", resp.StatusCode)
	}
	if err := s.metadata.stop(time.Second); err != nil {
		t.Fatal(err)
	}
	reopened, err := snapshothistory.Open(context.Background(), options)
	if err != nil {
		t.Fatal("shutdown retained archive lock")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal("served after shutdown")
	}
}
