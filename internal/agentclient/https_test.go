package agentclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPSAuthenticatesBeforeSendingToken(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	binding := Binding{key.Public().(ed25519.PublicKey), "11111111-1111-4111-8111-111111111111", 1, "docker", "unix:///run/docker.sock", "daemon"}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	var received atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/v1/session" {
			t.Error("wrong credential or path")
		}
		http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	addr := strings.TrimPrefix(s.URL, "https://")
	sum := sha256.Sum256(s.Certificate().Raw)
	for _, pin := range []string{strings.Repeat("0", 64), hex.EncodeToString(sum[:])} {
		c, err := NewHTTPS(context.Background(), addr, pin, token, binding)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Session(context.Background())
		wantMismatch := pin != hex.EncodeToString(sum[:])
		if !errors.Is(err, ErrProtocol) || errors.Is(err, ErrTLSPinMismatch) != wantMismatch || err.Error() != ErrProtocol.Error() {
			t.Error("session error classification or redaction changed")
		}
		if _, err := c.Session(context.Background()); err != ErrProtocol {
			t.Error("session did not retire the failed client")
		}
		c.Close()
		var want int32
		if !wantMismatch {
			want = 1
		}
		if got := received.Load(); got != want {
			t.Fatalf("server got %d requests; want %d", got, want)
		}
	}
}
