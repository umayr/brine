package web

import (
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbindings"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackAdmissionPersistentDenial(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "quarantined", true: "missing-ledger"}[missing], func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			q, err := secretbindings.NewQuarantine(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.Begin(secretbindings.DeploymentID("demo")); err != nil {
				t.Fatal(err)
			}
			q, err = secretbindings.OpenQuarantine(root)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				if err = os.Remove(filepath.Join(root, "quarantine.json")); err != nil {
					t.Fatal(err)
				}
			}
			runner := dill.New("must-not-execute")
			runner.ConfigureQuarantine(q)
			h := Handler{agent: agent.NewLocalAgent(nil, runner)}
			for i := 0; i < 2; i++ {
				w := httptest.NewRecorder()
				release, ok := h.admitStack(w, httptest.NewRequest("POST", "/", nil), "demo")
				if ok || release != nil || w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "quarantine") {
					t.Fatalf("admission: %v %d %s", ok, w.Code, w.Body.String())
				}
			}
		})
	}
}
