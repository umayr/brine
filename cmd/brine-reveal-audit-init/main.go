// Command brine-reveal-audit-init explicitly initializes a new reveal journal.
// Never rerun after uncertain initialization without manual reconciliation.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/umayr/brine/internal/revealaudit"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: brine-reveal-audit-init EXISTING_PRIVATE_DIRECTORY")
		os.Exit(2)
	}
	if _, err := revealaudit.Initialize(context.Background(), os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "audit initialization failed; inspect and reconcile state before retrying")
		os.Exit(1)
	}
	fmt.Println("audit initialized")
}
