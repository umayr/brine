package config

import (
	"fmt"

	"github.com/umayr/brine/internal/operationhistory"
	"github.com/umayr/brine/internal/target"
)

func (c *Config) validateOperationHistory() error {
	if c.OperationStateDir == "" {
		return nil
	}
	if c.Profile != target.Alpha || c.NoAuth || !c.QualifiedOperations() {
		return fmt.Errorf("operation history requires authenticated configured qualified Alpha")
	}
	if operationhistory.SeparateRoots(c.OperationStateDir, c.DataDir, c.CoordinationStateDir) != nil {
		return fmt.Errorf("operation history must be canonical and separate from editable data and coordination state in both directions")
	}
	return validateCoordinationState(c.OperationStateDir)
}
