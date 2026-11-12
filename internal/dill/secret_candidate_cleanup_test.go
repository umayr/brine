//go:build unix

package dill

import (
	"errors"
	"fmt"
	"testing"
)

func TestExpectedRetainedNetworkOutput(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		name := "Docker"
		if engine == "podman" {
			name = "Podman"
		}
		stack := "brine-ci-123"
		message := fmt.Sprintf("remove network (resource may be retained) %q (id ): network %q retained: %s resolves deletion targets as names or IDs and cannot guarantee identity-safe removal; remove manually after verifying ownership", stack, stack, name)
		fatal := fmt.Sprintf("ERRO fatal err=%q\n", message)
		progress := fmt.Sprintf("  %-14s %s\n", "removing", "app")
		fail := errors.New("lifecycle failed")
		cases := []struct {
			name, output string
			err          error
			want         bool
		}{
			{"retained", fatal, fail, true}, {"progress", progress + fatal, fail, true},
			{"success", fatal, nil, false}, {"other error", "ERRO fatal err=other\n", fail, false},
			{"extra error", fatal + "ERRO other\n", fail, false}, {"joined error", fmt.Sprintf("ERRO fatal err=%q\n", "container removal failed\n"+message), fail, false},
			{"other progress", "  removing       unexpected\n" + fatal, fail, false}, {"wrong stack", fatal, fail, false},
		}
		for _, tc := range cases {
			t.Run(engine+"/"+tc.name, func(t *testing.T) {
				target := stack
				if tc.name == "wrong stack" {
					target = "other"
				}
				if got := expectedRetainedNetworkOutput(engine, target, tc.output, tc.err); got != tc.want {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			})
		}
	}
	if expectedRetainedNetworkOutput("other", "x", "", errors.New("failed")) {
		t.Fatal("unknown engine accepted")
	}
}
