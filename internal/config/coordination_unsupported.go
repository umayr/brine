//go:build !linux

package config

import "fmt"

func validateCoordinationState(string) error {
	return fmt.Errorf("qualified operations require native Linux Docker")
}
