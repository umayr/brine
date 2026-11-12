package dill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretCapabilitiesStrictDiscovery(t *testing.T) {
	valid := `{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file","podman-host-file"]}`
	for _, tc := range []struct {
		name, data, engine string
		want               bool
	}{
		{"docker", valid, "docker", true}, {"podman", valid, "podman", true},
		{"old", `{}`, "docker", false}, {"duplicate", `{"contract":"x","contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"]}`, "docker", false},
		{"extra", `{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file"],"other":1}`, "docker", false},
		{"trailing", valid + ` {}`, "docker", false}, {"wrong-engine", valid, "other", false},
		{"invalid-utf8", valid + "\xff", "docker", false},
		{"null-engine", strings.Replace(valid, `"podman-host-file"`, `null`, 1), "docker", false},
		{"non-string-engine", strings.Replace(valid, `"podman-host-file"`, `3`, 1), "docker", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validSecretCapabilities([]byte(tc.data), tc.engine); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestSecretCapabilitiesProcessIsolation(t *testing.T) {
	t.Setenv("BRINE_SYNTHETIC_SECRET", "synthetic-canary")
	dir := t.TempDir()
	binary := filepath.Join(dir, "dill")
	script := `#!/bin/sh
if [ -n "$BRINE_SYNTHETIC_SECRET" ]; then exit 1; fi
printf '%s' '{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file","podman-host-file"]}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := New(binary).CheckSecretCapabilities(context.Background(), "docker"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho synthetic-canary >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := New(binary).CheckSecretCapabilities(context.Background(), "docker"); err != ErrSecretCapabilities {
		t.Fatalf("unexpected error: %v", err)
	}
}
