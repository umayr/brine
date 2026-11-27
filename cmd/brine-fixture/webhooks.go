package main

import (
	"bytes"
	"context"
	"path/filepath"

	"github.com/umayr/brine/internal/web"
	"github.com/umayr/brine/internal/webhooks"
)

// Only the explicit synthetic fixture creates a known non-secret key. Production
// startup must require an independently provisioned encryption key instead.
func enableFixtureWebhooks(h *web.Handler, dir string) (*webhooks.Service, error) {
	s, e := webhooks.Open(webhooks.Config{Directory: filepath.Join(dir, "webhooks"), EncryptionKey: bytes.Repeat([]byte{7}, 32)})
	if e != nil {
		return nil, e
	}
	h.EnableWebhooks(s, fixtureHookDispatcher{})
	return s, nil
}

type fixtureHookDispatcher struct{}

func (fixtureHookDispatcher) Validate(_ context.Context, t webhooks.Trigger) error {
	if t.StackID != "fixture-homepage" || t.TargetID != "fixture-local" || !containsFixtureAction(t.Action) {
		return webhooks.ErrUnsupported
	}
	return nil
}
func containsFixtureAction(a string) bool {
	for _, v := range webhooks.Actions {
		if a == v {
			return true
		}
	}
	return false
}
func (fixtureHookDispatcher) Execute(context.Context, webhooks.Trigger, webhooks.IncomingRequest) webhooks.TriggerResult {
	return webhooks.TriggerResult{Status: "rejected", Reason: "synthetic_fixture_never_executes"}
}
func (fixtureHookDispatcher) WebhookOptions(context.Context) ([]web.WebhookOption, error) {
	return []web.WebhookOption{{StackID: "fixture-homepage", StackName: "homepage", TargetID: "fixture-local", TargetName: "Local (synthetic)", Actions: webhooks.Actions}}, nil
}
