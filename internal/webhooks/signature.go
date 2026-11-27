package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

const (
	minSecretBytes  = 32
	signaturePrefix = "v1="
	maxSkewSeconds  = 5 * 60
)

var errInvalidSignature = errors.New("invalid webhook signature")

// Sign returns the v1 signature over timestamp, a dot, and the raw body.
// It returns an empty string if the secret is shorter than 32 bytes.
func Sign(secret []byte, timestamp string, body []byte) string {
	if len(secret) < minSecretBytes {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// Verify authenticates the webhook and checks its timestamp against now.
// The caller is responsible for replay protection.
func Verify(secret []byte, timestamp, signature string, body []byte, now time.Time) error {
	if len(secret) < minSecretBytes || len(signature) != len(signaturePrefix)+2*sha256.Size {
		return errInvalidSignature
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(seconds, 10) != timestamp {
		return errInvalidSignature
	}
	current := now.Unix()
	// Ordered unsigned subtraction preserves the distance even across zero.
	if seconds <= current {
		age := uint64(current) - uint64(seconds)
		if age > maxSkewSeconds || (age == maxSkewSeconds && now.Nanosecond() != 0) {
			return errInvalidSignature
		}
	} else if uint64(seconds)-uint64(current) > maxSkewSeconds {
		return errInvalidSignature
	}
	// A match against the canonical encoding also enforces the prefix and case.
	if !hmac.Equal([]byte(signature), []byte(Sign(secret, timestamp, body))) {
		return errInvalidSignature
	}
	return nil
}
