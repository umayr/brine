package config

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/umayr/brine/internal/webhooks"
)

var ErrWebhookSettings = errors.New("webhooks require explicit enablement, authentication, an absolute private state directory and a separate externally provisioned key file")

func withinPath(path, root string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
func (c *Config) validateWebhooks() error {
	if !c.WebhooksEnabled {
		if c.WebhooksDir != "" || c.WebhooksKeyFile != "" || len(c.WebhooksAllowPrivateCIDRs) > 0 || c.WebhooksMaxPending != 0 || c.WebhooksRetention != 0 {
			return ErrWebhookSettings
		}
		return nil
	}
	valid := func(p string) bool {
		return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && !strings.ContainsAny(p, "\x00\r\n")
	}
	if c.NoAuth || !valid(c.WebhooksDir) || !valid(c.WebhooksKeyFile) || !valid(c.DataDir) || withinPath(c.WebhooksKeyFile, c.DataDir) || withinPath(c.WebhooksKeyFile, c.WebhooksDir) || withinPath(c.WebhooksDir, c.WebhooksKeyFile) {
		return ErrWebhookSettings
	}
	if c.WebhooksMaxPending < 0 || c.WebhooksMaxPending > 100000 {
		return ErrWebhookSettings
	}
	if c.WebhooksRetention != 0 && (c.WebhooksRetention < 24*time.Hour || c.WebhooksRetention > 90*24*time.Hour) {
		return ErrWebhookSettings
	}
	client, e := webhooks.NewDeliveryClient(c.WebhooksAllowPrivateCIDRs)
	if e != nil {
		return ErrWebhookSettings
	}
	client.CloseIdleConnections()
	return nil
}
