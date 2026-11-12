package dill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretCapabilitiesSubprocessOutputLimit(t *testing.T) {
	valid := `{"contract":"dill.alpha-secrets/1","bindings":"dill.secret-bindings/1","policy":"dill.secret-policy/1","engines":["docker-host-file","podman-host-file"]}`
	for _, size := range []int{8192, 8193, 20000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "dill")
			output := valid + strings.Repeat(" ", size-len(valid))
			script := "#!/bin/sh\nprintf '%s' '" + output + "'\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			err := New(bin).CheckSecretCapabilities(context.Background(), "docker")
			if (err == nil) != (size <= 8192) {
				t.Fatalf("size %d: %v", size, err)
			}
		})
	}
}
