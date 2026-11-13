package dill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeHomesArePrivateIsolatedAndRemoved(t *testing.T) {
	operator := t.TempDir()
	t.Setenv("HOME", operator)
	t.Setenv("XDG_CONFIG_HOME", operator)
	t.Setenv("XDG_CACHE_HOME", operator)
	t.Setenv("DILL_SECRET_TARGET", "synthetic-server-target")
	if err := os.WriteFile(filepath.Join(operator, "credential-canary"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	log := filepath.Join(base, "homes")
	binary := filepath.Join(base, "dill")
	script := `#!/bin/sh
[ -n "$HOME" ] && [ -d "$HOME" ] || exit 30
[ ! -e "$HOME/credential-canary" ] && [ ! -e "$HOME/previous" ] || exit 31
[ "$XDG_CONFIG_HOME" = "$HOME/config" ] && [ "$XDG_CACHE_HOME" = "$HOME/cache" ] || exit 32
[ -z "$DILL_SECRET_TARGET" ] || exit 33
printf '%s\n' "$HOME" >> '` + log + `'
touch "$HOME/previous"
case " $* " in
 *" secret-capabilities "*) echo '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}' ;;
 *" validate "*) echo '{"valid":true}' ;;
 *" fmt "*) echo formatted ;;
 *" failure "*) exit 2 ;;
 *) echo '{}' ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := New(binary)
	r.Engine = "docker"
	r.Socket = "/synthetic/engine.sock"
	r.ResolveStack = func(string) (string, error) { return "demo", nil }
	ctx := context.Background()
	if _, err := r.capture(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateContent(ctx, "/synthetic/compose.pkl", "synthetic"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Fmt(ctx, "/synthetic/compose.pkl", "synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckSecretCapabilities(ctx, "docker"); err != nil {
		t.Fatal(err)
	}
	s := &SecretRunner{Runner: r, Policy: "/synthetic/policy"}
	if _, _, err := s.capture(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.capture(ctx, "failure"); err == nil {
		t.Fatal("failure accepted")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	homes := strings.Fields(string(data))
	if len(homes) != 6 {
		t.Fatalf("expected 6 runtime homes, got %d", len(homes))
	}
	seen := map[string]bool{}
	for _, home := range homes {
		if home == operator || !filepath.IsAbs(home) || seen[home] {
			t.Fatal("runtime home inherited or reused")
		}
		seen[home] = true
		if _, err = os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("runtime home not removed")
		}
	}
}

func TestRuntimeHomeCleanupOnCancellationAndStartFailure(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	r := New(filepath.Join(temp, "missing-dill"))
	if _, err := r.capture(context.Background(), "version"); err == nil {
		t.Fatal("missing executable accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = New("/bin/sleep")
	if _, err := r.capture(ctx, "5"); err == nil {
		t.Fatal("cancelled process accepted")
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "brine-dill-home-") {
			t.Fatal("runtime home leaked")
		}
	}
}

func TestRuntimeHomeRemovedAfterRunningProcessCancellation(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "home")
	binary := filepath.Join(dir, "dill")
	script := "#!/bin/sh\nprintf '%s' \"$HOME\" > '" + log + "'\nexec /bin/sleep 30\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := New(binary).capture(ctx, "test"); done <- err }()
	var home []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(log)
		if err == nil && len(data) > 0 {
			home = data
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(home) == 0 {
		t.Fatal("subprocess did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled process accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled process did not exit")
	}
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Fatal("runtime home leaked after cancellation")
	}
}

func TestRuntimeHomeCreationFailureFailsClosed(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	r := New("/usr/bin/true")
	if _, err := r.capture(context.Background(), "test"); err == nil {
		t.Fatal("runtime home creation failure ignored")
	}
	if err := r.CheckSecretCapabilities(context.Background(), "docker"); err != ErrSecretCapabilities {
		t.Fatalf("capability failure: %v", err)
	}
	s := &SecretRunner{Runner: r, Policy: "/synthetic/policy"}
	if _, _, err := s.capture(context.Background(), "test"); err != ErrSecretUnavailable {
		t.Fatalf("secret failure: %v", err)
	}
}

func TestSecretRunnerUsesExplicitPklOverrideWithoutDuplicateEnvironment(t *testing.T) {
	r := New("/usr/bin/true")
	r.PklBinary = "/synthetic/runner-pkl"
	s := &SecretRunner{Runner: r, Policy: "/synthetic/policy", PklBinary: "/synthetic/secret-pkl"}
	cmd, cleanup, err := s.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	count := 0
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "DILL_PKL_PATH=") {
			count++
			if entry != "DILL_PKL_PATH=/synthetic/secret-pkl" {
				t.Fatal("incorrect Pkl path")
			}
		}
	}
	if count != 1 {
		t.Fatal("duplicate or missing Pkl path")
	}
}

func TestRuntimeHomeIsPrivateAndInitiallyEmpty(t *testing.T) {
	r := New("/usr/bin/true")
	cmd, cleanup, err := r.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var home string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "HOME=") {
			home = strings.TrimPrefix(e, "HOME=")
		}
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("runtime home not private")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("runtime home is not empty")
	}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
