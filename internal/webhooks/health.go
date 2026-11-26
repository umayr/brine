package webhooks

// Health describes worker liveness only. It never attests to target state,
// delivery success or execution success. A stopped worker does not empty queues.
type Health struct {
	Delivery string `json:"delivery"`
	Incoming string `json:"incoming"`
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
	return h
}
