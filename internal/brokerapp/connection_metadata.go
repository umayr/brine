package brokerapp

import (
	"github.com/umayr/brine/internal/secretbroker"
	"github.com/umayr/brine/internal/secrets"
)

func (s *Service) ConnectionsFor(scope secretbroker.Scope) []Connection {
	out := []Connection{}
	for _, r := range s.policy.Registrations {
		if r.Scope == scope {
			for _, c := range s.Connections() {
				for _, id := range r.Connections {
					if id == c.ID {
						out = append(out, c)
					}
				}
			}
		}
	}
	return out
}
func (s *Service) ConnectionKind(id string) (secretbroker.ProviderKind, bool) {
	if !s.Enabled() {
		return "", false
	}
	c, err := s.registry.Capabilities(id)
	if err != nil {
		return "", false
	}
	switch c.Kind {
	case secrets.KindVault:
		return secretbroker.ProviderVault, true
	case secrets.KindInfisical:
		return secretbroker.ProviderInfisical, true
	case secrets.KindFile:
		return secretbroker.ProviderLocal, true
	}
	return "", false
}
