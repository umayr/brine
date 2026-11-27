package server

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/brokerapp"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/secretbroker"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerLegacyGatesResolveNamesButPreservePaths(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Dir(data), 0700); err != nil {
		t.Fatal(err)
	}
	if err := brokerapp.Initialize(data); err != nil {
		t.Fatal(err)
	}
	b, err := brokerapp.Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.State.Register(secretbroker.Scope{RegistrationID: strings.Repeat("a", 32), StackName: "protected", TargetRegistration: "local"}, "operator"); err != nil {
		t.Fatal(err)
	}
	oldCalls := 0
	sentinel := errors.New("previous gate")
	r := &dill.Runner{ResolveStack: func(path string) (string, error) {
		switch path {
		case "/owned/plain/compose.pkl":
			return "plain", nil
		case "/owned/unrelated-directory/compose.pkl":
			return "protected", nil
		}
		return "", errors.New("unowned")
	}}
	previous := func(_ context.Context, path string) error {
		oldCalls++
		if path != "/owned/plain/compose.pkl" {
			t.Fatal("old gate lost path")
		}
		return sentinel
	}
	r.BeforeLegacyAction, r.BeforeLegacyValidate = previous, previous
	installBrokerLegacyGates(r, b)
	for _, gate := range []func(context.Context, string) error{r.BeforeLegacyAction, r.BeforeLegacyValidate} {
		if !errors.Is(gate(context.Background(), "/owned/plain/compose.pkl"), sentinel) {
			t.Fatal("plain sibling rejected or previous gate skipped")
		}
		before := oldCalls
		for _, path := range []string{"/owned/unrelated-directory/compose.pkl", "/unknown/compose.pkl"} {
			if gate(context.Background(), path) == nil {
				t.Fatal("protected/unowned source allowed")
			}
		}
		if oldCalls != before {
			t.Fatal("protected source reached previous gate")
		}
	}
	before := oldCalls
	r.ResolveStack = nil
	if r.BeforeLegacyAction(context.Background(), "/owned/plain/compose.pkl") == nil {
		t.Fatal("missing resolver allowed")
	}
	if oldCalls != before {
		t.Fatal("missing resolver reached previous gate")
	}
	r.ResolveStack = func(string) (string, error) { return "", nil }
	if r.BeforeLegacyAction(context.Background(), "/owned/plain/compose.pkl") == nil || oldCalls != before {
		t.Fatal("empty resolver reached previous gate")
	}
	r.ResolveStack = func(string) (string, error) { return "plain", nil }
	b.Close()
	if r.BeforeLegacyValidate(context.Background(), "/owned/plain/compose.pkl") == nil {
		t.Fatal("unreadable broker state allowed")
	}
	if oldCalls != before {
		t.Fatal("unreadable broker state reached previous gate")
	}
}
