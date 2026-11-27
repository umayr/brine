package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func referenceSignature(key []byte, message string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return fmt.Sprintf("v1=%x", mac.Sum(nil))
}

func TestSign(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("\x00raw.body\xff\n")
	want := referenceSignature(key, "1700000000.\x00raw.body\xff\n")
	if got := Sign(key, "1700000000", body); got != want {
		t.Fatalf("Sign() = %q, want %q", got, want)
	}
	for _, badKey := range [][]byte{nil, key[:31]} {
		if got := Sign(badKey, "1700000000", body); got != "" {
			t.Errorf("Sign accepted a %d-byte key", len(badKey))
		}
	}
}

func TestVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	const timestamp, body = "1700000000", "\x00raw.body\xff\n"
	now := time.Unix(1700000000, 0)
	sig := referenceSignature(key, timestamp+"."+body)
	for _, tt := range []struct {
		name, timestamp, signature, body string
		key                              []byte
		valid                            bool
	}{
		{"valid", timestamp, sig, body, key, true},
		{"tampered body", timestamp, sig, body + " ", key, false},
		{"tampered timestamp", "1700000001", sig, body, key, false},
		{"wrong digest", timestamp, "v1=" + strings.Repeat("0", 64), body, key, false},
		{"wrong prefix", timestamp, "v2=" + sig[3:], body, key, false},
		{"uppercase hex", timestamp, "v1=" + strings.ToUpper(sig[3:]), body, key, false},
		{"nonhex", timestamp, "v1=g" + sig[4:], body, key, false},
		{"short", timestamp, sig[:len(sig)-1], body, key, false},
		{"long", timestamp, sig + "0", body, key, false},
		{"empty signature", timestamp, "", body, key, false},
		{"empty key", timestamp, referenceSignature(nil, timestamp+"."+body), body, nil, false},
		{"short key", timestamp, referenceSignature(key[:31], timestamp+"."+body), body, key[:31], false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(tt.key, tt.timestamp, tt.signature, []byte(tt.body), now); (err == nil) != tt.valid {
				t.Fatalf("Verify() = %v, want valid = %t", err, tt.valid)
			}
		})
	}
}

func TestVerifyTimestamp(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, tt := range []struct {
		name      string
		timestamp string
		now       time.Time
		valid     bool
	}{
		{"old boundary", strconv.Itoa(700), time.Unix(1000, 0), true},
		{"future boundary", "1300", time.Unix(1000, 0), true},
		{"stale", "699", time.Unix(1000, 0), false},
		{"too far in future", "1301", time.Unix(1000, 0), false},
		{"old by one nanosecond", "700", time.Unix(1000, 1), false},
		{"future by one nanosecond", "1300", time.Unix(999, 999999999), false},
		{"zero", "0", time.Unix(0, 0), true},
		{"negative", "-1", time.Unix(0, 0), true},
		{"negative zero", "-0", time.Unix(0, 0), false},
		{"leading zero", "01000", time.Unix(1000, 0), false},
		{"plus sign", "+1000", time.Unix(1000, 0), false},
		{"leading space", " 1000", time.Unix(1000, 0), false},
		{"trailing newline", "1000\n", time.Unix(1000, 0), false},
		{"fractional", "1000.0", time.Unix(1000, 0), false},
		{"empty timestamp", "", time.Unix(0, 0), false},
		{"overflow", "9223372036854775808", time.Unix(1000, 0), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sig := referenceSignature(key, tt.timestamp+".")
			if err := Verify(key, tt.timestamp, sig, nil, tt.now); (err == nil) != tt.valid {
				t.Fatalf("Verify() = %v, want valid = %t", err, tt.valid)
			}
		})
	}
}
