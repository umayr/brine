package agentclient

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/netip"
	"time"
)

// NewHTTPS opens a read client for an already enrolled private address. The
// out-of-band pin is SHA-256 of the agent leaf certificate's DER, in lowercase
// hex. The credential is a canonical 32-byte base64url token. Neither value is
// discovered from the remote listener. Session still checks the engine binding.
func NewHTTPS(lifetime context.Context, address, pin, token string, b Binding) (*Client, error) {
	addr, addrErr := netip.ParseAddrPort(address)
	want, pinErr := hex.DecodeString(pin)
	key, keyErr := base64.RawURLEncoding.Strict().DecodeString(token)
	if lifetime == nil || lifetime.Err() != nil || !validBinding(b) || addrErr != nil || addr.Port() == 0 || addr.Addr().Zone() != "" ||
		!(addr.Addr().Unmap().IsPrivate() || addr.Addr().Unmap().IsLoopback()) || pinErr != nil || len(want) != sha256.Size || hex.EncodeToString(want) != pin ||
		keyErr != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != token {
		return nil, ErrProtocol
	}
	t := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Exact out-of-band leaf pin replaces hostname and CA discovery. The
		// TLS handshake still proves possession of the matching private key.
		InsecureSkipVerify: true,
		VerifyConnection: func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 {
				return ErrProtocol
			}
			leaf := s.PeerCertificates[0]
			got := sha256.Sum256(leaf.Raw)
			now := time.Now()
			if subtle.ConstantTimeCompare(got[:], want) != 1 || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
				return ErrProtocol
			}
			return nil
		},
	}}
	return newClient(lifetime, b, "https://"+addr.String(), token, "tcp", addr.String(), t), nil
}
