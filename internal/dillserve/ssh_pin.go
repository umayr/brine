//go:build darwin || linux || freebsd || openbsd || netbsd

package dillserve

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// PinnedSSHOptions is a separate, dormant transport API. PublicKey must be a
// canonical, comment-free OpenSSH ssh-ed25519 public key; Fingerprint is its
// independently reviewed OpenSSH SHA256 fingerprint. KnownHosts is not used.
type PinnedSSHOptions struct {
	SSHOptions
	PublicKey, Fingerprint string
}

// NewPinnedSSH starts a tunnel whose host key is restricted to the supplied pin.
// It does not verify a projectreview record or enable snapshot staging.
func NewPinnedSSH(ctx context.Context, o PinnedSSHOptions) (*SSHTunnel, error) {
	return newPinnedSSH(ctx, o, startSSH, 10*time.Second)
}

// ValidatePinnedSSH verifies the exact host-key pin without starting a process.
func ValidatePinnedSSH(o PinnedSSHOptions) error {
	if o.KnownHosts != "" || !sshUser.MatchString(o.User) || !safeSSHFile(o.IdentityFile) || !safeSSHPath(o.RemoteSocket) || o.Stack == "" || o.Engine == "" {
		return ErrUnavailable
	}
	_, err := pinnedEntry(o.Host, o.Port, o.PublicKey, o.Fingerprint)
	return err
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func pinnedEntry(host string, port uint16, key, fingerprint string) (string, error) {
	// Restrict input syntax, not resolver behavior: short names, aliases and
	// alternate numeric address forms can still be interpreted by the resolver.
	if len(host) == 0 || len(host) > 253 || net.ParseIP(host) != nil || strings.Trim(host, "0123456789.") == "" || port == 0 {
		return "", ErrUnavailable
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(label) {
			return "", ErrUnavailable
		}
	}
	const prefix = "ssh-ed25519 "
	if !strings.HasPrefix(key, prefix) {
		return "", ErrUnavailable
	}
	encoded := strings.TrimPrefix(key, prefix)
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	// SSH wire format: string algorithm, string exactly 32 bytes of public key.
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded || len(raw) != 51 || binary.BigEndian.Uint32(raw[:4]) != 11 || string(raw[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(raw[15:19]) != 32 {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256(raw)
	if fingerprint != "SHA256:"+base64.RawStdEncoding.EncodeToString(digest[:]) {
		return "", ErrUnavailable
	}
	lookup := host
	if port != 22 {
		lookup = fmt.Sprintf("[%s]:%d", host, port)
	}
	return lookup + " " + key + "\n", nil
}

func newPinnedSSH(ctx context.Context, o PinnedSSHOptions, start sshStarter, timeout time.Duration) (*SSHTunnel, error) {
	// Reject, rather than silently ignore, an ambient caller-provided file.
	if o.KnownHosts != "" {
		return nil, ErrUnavailable
	}
	entry, err := pinnedEntry(o.Host, o.Port, o.PublicKey, o.Fingerprint)
	if err != nil {
		return nil, ErrUnavailable
	}
	return newSSHWithPin(ctx, o.SSHOptions, entry, start, timeout)
}
