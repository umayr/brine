package main

import (
	"encoding/json"
	"errors"
	"flag"
	"github.com/umayr/brine/internal/secretbindings"
	"io"
)

// This local operator action uses the private data-root permission boundary,
// never HTTP authentication, and never starts an engine or replays execution.
func registerQuarantineFlags(fs *flag.FlagSet, root *string, out io.Writer) func() (bool, error) {
	action := fs.String("process-quarantine", "", "local operator action: list or recover (requires original execution host/data root)")
	stack := fs.String("quarantine-stack", "", "exact stack name for recovery")
	token := fs.String("quarantine-token", "", "exact saved intent token from list")
	inspected := fs.Bool("quarantine-inspected", false, "confirm target effects inspected; clearance does not imply success")
	sameHost := fs.Bool("quarantine-same-host", false, "confirm this is the original execution host and unrelocated data root")
	return func() (bool, error) {
		if *action == "" {
			if *stack != "" || *token != "" || *inspected || *sameHost {
				return true, errors.New("process-quarantine action required")
			}
			return false, nil
		}
		if *action != "list" && *action != "recover" {
			return true, errors.New("invalid process-quarantine action")
		}
		if *action == "recover" && (*stack == "" || *token == "" || !*inspected || !*sameHost) {
			return true, errors.New("recovery requires exact stack/token, target inspection and original-host confirmation")
		}
		q, err := secretbindings.OpenQuarantine(*root)
		if err != nil {
			return true, err
		}
		if *action == "list" {
			entries, err := q.List()
			if err != nil {
				return true, err
			}
			return true, json.NewEncoder(out).Encode(entries)
		}
		return true, q.RecoverAfterReboot(secretbindings.DeploymentID(*stack), *token, *inspected)
	}
}
