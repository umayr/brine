package git

import (
	"context"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dill"
)

func TestAlphaBackgroundDisabledWithQualifiedClient(t *testing.T) {
	for _, configured := range []bool{false, true} {
		r := dill.New("/must-not-run")
		r.Profile = "alpha"
		if configured {
			r.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/must-not-open", Layout: "native-local-docker-v1", DaemonID: "independently-known-daemon"}
		}
		r.CheckSource = func(string) error { t.Fatal("background source checked"); return nil }
		// nil store/git deliberately panic if either scheduler or direct sync gets
		// beyond the explicit Alpha gate. No pull, source evaluation or Up is allowed.
		s := NewSyncer(nil, nil, r, time.Second)
		s.syncAll(context.Background())
		s.sync(context.Background(), "demo")
	}
}
