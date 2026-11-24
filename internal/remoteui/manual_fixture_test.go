package remoteui

import "github.com/umayr/brine/internal/store"

// Metadata-failure adapters retain the independent durable recovery store.
func (s overlapErrorStore) ListManualIntents() ([]store.ManualDeploymentIntent, error) {
	return s.StackStore.(store.ManualDeploymentStore).ListManualIntents()
}
func (s overlapErrorStore) SaveManualIntent(i store.ManualDeploymentIntent) error {
	return s.StackStore.(store.ManualDeploymentStore).SaveManualIntent(i)
}
func (s overlapErrorStore) GetManualIntent(id string) (store.ManualDeploymentIntent, error) {
	return s.StackStore.(store.ManualDeploymentStore).GetManualIntent(id)
}
func (s overlapErrorStore) ResolveManualIntent(i store.ManualDeploymentIntent) error {
	return s.StackStore.(store.ManualDeploymentStore).ResolveManualIntent(i)
}
func (s *attendedStackFixture) ListManualIntents() ([]store.ManualDeploymentIntent, error) {
	return s.StackStore.(store.ManualDeploymentStore).ListManualIntents()
}
func (s *attendedStackFixture) SaveManualIntent(i store.ManualDeploymentIntent) error {
	return s.StackStore.(store.ManualDeploymentStore).SaveManualIntent(i)
}
func (s *attendedStackFixture) GetManualIntent(id string) (store.ManualDeploymentIntent, error) {
	return s.StackStore.(store.ManualDeploymentStore).GetManualIntent(id)
}
func (s *attendedStackFixture) ResolveManualIntent(i store.ManualDeploymentIntent) error {
	return s.StackStore.(store.ManualDeploymentStore).ResolveManualIntent(i)
}
func (s *blockedPollStore) ListManualIntents() ([]store.ManualDeploymentIntent, error) {
	return nil, nil
}
func (s *blockedPollStore) SaveManualIntent(store.ManualDeploymentIntent) error {
	return store.ErrManualIntent
}
func (s *blockedPollStore) GetManualIntent(string) (store.ManualDeploymentIntent, error) {
	return store.ManualDeploymentIntent{}, store.ErrManualIntent
}
func (s *blockedPollStore) ResolveManualIntent(store.ManualDeploymentIntent) error {
	return store.ErrManualIntent
}
