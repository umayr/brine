package brokerapp

func (c *Controller) Inspect(id, stack, session string) (PlanReview, error) {
	if c == nil {
		return PlanReview{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purge()
	r, ok := c.pending[id]
	if !ok || r.Identity.Scope.StackName != stack || r.Identity.SessionDigest != session {
		return PlanReview{}, ErrUnavailable
	}
	return copyReview(r), nil
}
