package dill

import (
	"context"
	"strings"
	"testing"
)

func TestCommandDoesNotInheritServerCredentials(t *testing.T) {
	for _, key := range []string{"BRINE_AUTH_USER", "BRINE_AUTH_PASSWORD", "BRINE_API_TOKEN"} {
		t.Setenv(key, "synthetic-auth-canary")
	}
	t.Setenv("BRINE_SYNTHETIC_DEPLOYMENT", "synthetic-deployment-canary")
	cmd := New("dill").command(context.Background(), "validate")
	env := strings.Join(cmd.Env, "\n")
	if strings.Contains(env, "synthetic-auth-canary") {
		t.Fatal("server credentials inherited")
	}
	if !strings.Contains(env, "BRINE_SYNTHETIC_DEPLOYMENT=synthetic-deployment-canary") {
		t.Fatal("deployment environment unexpectedly removed")
	}
}
