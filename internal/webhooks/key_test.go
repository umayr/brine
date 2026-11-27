package webhooks

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKeyPrivateExternalFile(t *testing.T) {
	for _, kind := range []string{"valid", "short", "long", "public", "symlink", "hardlink", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "key")
			key := bytes.Repeat([]byte{9}, 32)
			switch kind {
			case "valid":
				os.WriteFile(keyPath, key, 0600)
			case "short":
				os.WriteFile(keyPath, key[:31], 0600)
			case "long":
				os.WriteFile(keyPath, append(key, 0), 0600)
			case "public":
				os.WriteFile(keyPath, key, 0644)
			case "symlink":
				os.WriteFile(filepath.Join(dir, "original"), key, 0600)
				os.Symlink(filepath.Join(dir, "original"), keyPath)
			case "hardlink":
				os.WriteFile(filepath.Join(dir, "original"), key, 0600)
				os.Link(filepath.Join(dir, "original"), keyPath)
			case "directory":
				os.Mkdir(keyPath, 0700)
			}
			got, e := LoadKey(keyPath)
			if kind == "valid" {
				if e != nil || !bytes.Equal(got, key) {
					t.Fatal("valid key rejected", e)
				}
			} else if e == nil || got != nil {
				t.Fatal("unsafe key accepted")
			}
		})
	}
}
