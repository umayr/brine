package targetregistry

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReviewedPreviousCannotBeSubstituted(t *testing.T) {
	r := registry(t)
	old := record()
	if err := r.Add(old, old); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Revision++
	next.Host = "next.example"
	reviewed := ReviewedUpdate{Previous: old, Next: next}
	substituted := old
	substituted.Host = "different.example"
	if !errors.Is(r.Update(substituted, next, reviewed), ErrUnconfirmed) {
		t.Fatal("substituted previous accepted")
	}
	substitutedReview := reviewed
	substitutedReview.Previous = substituted
	if !errors.Is(r.Update(old, next, substitutedReview), ErrUnconfirmed) {
		t.Fatal("unreviewed old accepted")
	}
	got, err := r.Get(old.ID)
	if err != nil || got != old {
		t.Fatal(got, err)
	}
}
func TestReviewStringsRejectInvalidAndDeceptiveText(t *testing.T) {
	for _, suffix := range []string{string([]byte{0xff}), "\x1b", "\t", "\u202e", "\u2066", "\u200b", "\u2028", "\u034f", "\ufe0f", "é"} {
		original := record()
		rv := reflect.ValueOf(&original).Elem()
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).Kind() != reflect.String {
				continue
			}
			x := record()
			v := reflect.ValueOf(&x).Elem().Field(i)
			v.SetString(v.String() + suffix)
			if x.Validate() == nil {
				t.Fatalf("accepted %s in %s", suffix, rv.Type().Field(i).Name)
			}
		}
	}
	r := registry(t)
	x := record()
	x.KeyPath = "/valid/\xff"
	if r.Add(x, x) == nil {
		t.Fatal("invalid UTF-8 persisted")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "targets.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected file: %v", err)
	}
}
func TestRevealKeyFingerprintAndCopy(t *testing.T) {
	r := record()
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(key)
	r.RevealKeyFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	checked, err := r.VerifiedRevealKey(key)
	if err != nil {
		t.Fatal(err)
	}
	key[0] ^= 1
	if checked[0] == key[0] {
		t.Fatal("returned key aliases input")
	}
	if _, err = r.VerifiedRevealKey(key); !errors.Is(err, ErrMismatch) {
		t.Fatal("accepted wrong key", err)
	}
	if _, err = r.VerifiedRevealKey(key[:31]); !errors.Is(err, ErrMismatch) {
		t.Fatal("accepted short key", err)
	}
}
func TestExactPrintableASCIIBytesRoundTrip(t *testing.T) {
	r := registry(t)
	v := record()
	v.KeyPath = "/home/operator/key with 'quotes'"
	v.DaemonID = "daemon with \\\"quotes\\\""
	v.RemoteSocket = "/run/agent socket.sock"
	if err := r.Add(v, v); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(v.ID)
	if err != nil || got != v {
		t.Fatal(got, err)
	}
}
