package dill

import "github.com/umayr/brine/internal/brokerwire"

func (c *LocalBroker) Engine() string {
	if c.config.ProfilePath != "" {
		return c.config.Engine
	}
	return "docker"
}
func (c *LocalBroker) expectedTarget() string {
	if c.config.ProfilePath != "" {
		return c.config.TargetDigest
	}
	return brokerwire.Digest([]byte("dill-local-docker-v1\x00" + c.config.DaemonID))
}
func (c *LocalBroker) expectedEndpoint() string {
	if c.config.ProfilePath != "" {
		return c.config.EndpointDigest
	}
	return brokerwire.Digest([]byte("unix://" + c.config.Socket))
}
