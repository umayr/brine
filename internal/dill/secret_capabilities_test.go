package dill

import (
	"context"
	"os"
	"os/exec"
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

func TestSecretCapabilitiesDisabledBeforeDiscovery(t *testing.T) {
	dir := t.TempDir()
	binary, marker := filepath.Join(dir, "dill"), filepath.Join(dir, "called")
	script := "#!/bin/sh\nprintf 'called\\n' >> '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(binary, "positive").Run(); err != nil {
		t.Fatalf("fake positive control: %v", err)
	}
	before, err := os.ReadFile(marker)
	if err != nil || string(before) != "called\n" {
		t.Fatalf("fake did not write marker: %q %v", before, err)
	}
	for _, engine := range []string{"docker", "podman", "invalid"} {
		if err := New(binary).CheckSecretCapabilities(context.Background(), engine); err != ErrSecretUnavailable {
			t.Fatalf("%s: %v", engine, err)
		}
	}
	after, err := os.ReadFile(marker)
	if err != nil || string(after) != string(before) {
		t.Fatalf("discovery spawned child: %q %v", after, err)
	}
}
