package secretbindings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuarantineRejectsNoncanonicalIdentityEncoding(t *testing.T) {
	for _, key := range []string{"\"bad\xffkey\"", `"bad\ud800key"`, `"\u0073ynthetic"`} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			q, err := NewQuarantine(root)
			if err != nil {
				t.Fatal(err)
			}
			token, err := q.Begin("synthetic")
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("{" + key + ":\"" + token + "\"}")
			if err = os.WriteFile(filepath.Join(root, ledgerName), data, 0600); err != nil {
				t.Fatal(err)
			}
			if q.Check("synthetic") == nil {
				t.Fatal("corrupt identity treated as absent")
			}
			if _, err = OpenQuarantine(root); err == nil {
				t.Fatal("corrupt ledger reopened")
			}
			if _, err = q.Begin("other"); err == nil {
				t.Fatal("corrupt ledger allowed mutation")
			}
		})
	}
}
