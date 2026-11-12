package secretbindings

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeploymentID derives an internal, path-safe key from Brine's stack identifier.
func DeploymentID(stack string) string {
	sum := sha256.Sum256([]byte(stack))
	return "deployment-" + hex.EncodeToString(sum[:])[:32]
}
