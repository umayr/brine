package web

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/umayr/brine/internal/webhooks"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type hooksPartialDispatcher struct{ hooksFixtureDispatcher }

func (hooksPartialDispatcher) WebhookOptions(context.Context) ([]WebhookOption, error) {
	return []WebhookOption{{StackID: "valid-local", TargetID: "local", StackName: "Eligible local", TargetName: "Local", Actions: []string{"plan"}}}, webhooks.ErrPartialInventory
}
func TestHooksPartialDiscoveryRetainsValidOptions(t *testing.T) {
	s, e := webhooks.Open(webhooks.Config{Directory: filepath.Join(t.TempDir(), "hooks"), EncryptionKey: bytes.Repeat([]byte{1}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := &Handler{}
	h.EnableWebhooks(s, hooksPartialDispatcher{})
	w := httptest.NewRecorder()
	h.hooksInventory(w, httptest.NewRequest("GET", "/api/webhooks/", nil))
	var got struct {
		Options   []WebhookOption
		Available bool `json:"options_available"`
		Partial   bool `json:"options_partial"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &got); e != nil || w.Code != 200 || !got.Available || !got.Partial || len(got.Options) != 1 || got.Options[0].StackID != "valid-local" {
		t.Fatal("partial discovery discarded valid authority", w.Code, e, w.Body.String())
	}
}
