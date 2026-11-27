//go:build linux

package brokerapp

import (
	"os"
	"testing"
)

func TestBrokerRuntimePodmanFile(t *testing.T) {
	if os.Getenv("BRINE_BROKER_RUNTIME_TEST") != "1" || os.Getenv("BROKER_TEST_PROFILE") == "" {
		t.Skip("explicit qualified Podman fixture required")
	}
	TestBrokerRuntimeLocalFile(t)
}
