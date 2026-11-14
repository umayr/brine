//go:build linux

package dill

import (
	"strings"
	"testing"
)

func TestLinuxSyntheticPreflightScopeAndForceGrammar(t *testing.T) {
	c := PreflightArguments{Operation: "up", ConfigFile: "/synthetic/export.pkl", Socket: "unix:///synthetic/pinned.sock", Stack: "synthetic", Coordination: QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "independent-daemon"}, ForceRecreate: true}
	digest := "sha256:" + strings.Repeat("a", 64)
	args, err := BuildBoundPreflightArguments(c, digest)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, s := range []string{" --force-recreate ", " --preflight-contract dill.preflight/1 ", " --expect-preflight " + digest + " "} {
		if !strings.Contains(joined, s) {
			t.Fatalf("missing %s", s)
		}
	}
	for _, extra := range [][]string{{"web"}, {"--no-deps"}, {"--service", "web"}, {"--volumes"}} {
		bad := append(append([]string{}, args...), extra...)
		if ValidatePreflightArguments(bad) == nil {
			t.Fatalf("accepted %v", extra)
		}
	}
	for _, op := range []string{"rm", "teardown", "plan", "diff", "validate"} {
		c.Operation = op
		if _, err = BuildPreflightArguments(c); err == nil {
			t.Fatalf("accepted %s", op)
		}
	}
}
