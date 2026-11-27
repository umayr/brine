package webhooks

// Health describes worker liveness only. It never attests to target state,
// delivery success or execution success. A stopped worker does not empty queues.
type Health struct {
	Delivery     string `json:"delivery"`
	Incoming     string `json:"incoming"`
	Observations string `json:"observations"`
}

func (s *Service) Health() Health {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	h := Health{Delivery: "stopped", Incoming: "stopped"}
	if s.running.Load() {
		h.Delivery = "running"
	}
	if s.incomingRunning {
		h.Incoming = "running"
	}
	h.Observations = "disabled"
	if s.sample != nil {
		h.Observations = "stopped"
		switch s.samplingStatus.Load() {
		case 1:
			h.Observations = "running"
		case 2:
			h.Observations = "unavailable"
		}
	}
	return h
}
