package remoteagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

func TestHTTPSOpenClassifiesOnlyLeafPinMismatch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "synthetic detail that must not escape", http.StatusUnauthorized)
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "https://")
	pin := sha256.Sum256(server.Certificate().Raw)
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))

	for _, tt := range []struct {
		name, pin    string
		wantMismatch bool
		wantRequests int32
	}{
		{"wrong leaf pin", strings.Repeat("0", 64), true, 0},
		{"authenticated transport with rejected credential", hex.EncodeToString(pin[:]), false, 1},
		{"closed transport", hex.EncodeToString(pin[:]), false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "closed transport" {
				server.Close()
			}
			requests.Store(0)
			bundle := fmt.Sprintf(`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"daemon"}`, tt.pin, token, hex.EncodeToString(pub))
			record := targetregistry.Record{ID: "synthetic", Revision: 1, HTTPSAddress: addr, HTTPSPairing: targetregistry.PairingSecret(bundle)}
			session, err := Open(context.Background(), record, Config{})
			if session != nil {
				_ = session.Close()
				t.Fatal("unexpected usable session")
			}
			if err == nil || err.Error() != "remoteagent: target unavailable" || errors.Is(err, agentclient.ErrTLSPinMismatch) != tt.wantMismatch {
				t.Fatal("unexpected classification or unredacted error")
			}
			if requests.Load() != tt.wantRequests {
				t.Fatal("unexpected HTTP delivery or retry")
			}
		})
	}
}
