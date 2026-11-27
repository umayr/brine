package agentclient

import "github.com/umayr/brine/internal/brokerwire"

func (c *BrokerClient) Engine() string { return c.pair.deploy.binding.EngineKind }
func (c *BrokerClient) expectedBrokerTarget() string {
	if c.targetDigest != "" {
		return c.targetDigest
	}
	return brokerwire.Digest([]byte("dill-local-docker-v1\x00" + c.pair.deploy.binding.DaemonID))
}
