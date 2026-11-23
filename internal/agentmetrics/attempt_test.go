package agentmetrics

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

const candidateKATSHA = "ca2019477a750f0e0457ff321891dbbc4b67a751dbec7a704243dbf98f379784"

type receiptKAT struct {
	PublicKeyHex, AgentID, EngineID, Epoch string
	Vectors                                []struct {
		Case, Path, RequestID, Generation, BodyHex, PreimageHex, BodySHA256, Signature string
	}
}

func candidateReceipts(t *testing.T) receiptKAT {
	t.Helper()
	raw, err := os.ReadFile("testdata/aggregate-signing-candidate-kat.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != candidateKATSHA {
		t.Fatal("candidate fixture digest mismatch")
	}
	var f receiptKAT
	if json.Unmarshal(raw, &f) != nil || len(f.Vectors) != 3 {
		t.Fatal("invalid candidate fixture")
	}
	return f
}

func receiptHex(t *testing.T, text string) []byte {
	t.Helper()
	v, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal("invalid fixture hex")
	}
	return v
}

func receiptInput(t *testing.T, f receiptKAT, i int) (Context, []string, []byte) {
	t.Helper()
	v := f.Vectors[i]
	generation, err := strconv.ParseUint(v.Generation, 10, 64)
	if err != nil {
		t.Fatal("invalid exact fixture generation")
	}
	return Context{
		OptIn: true, CallerAuthorized: true,
		SessionVerified: true, AttemptActive: true,
		Method: "POST", Route: v.Path, RequestID: v.RequestID,
		AgentID: f.AgentID, EngineID: f.EngineID, Epoch: f.Epoch,
		Generation: generation, PublicKey: receiptHex(t, f.PublicKeyHex),
	}, []string{v.Signature}, receiptHex(t, v.BodyHex)
}

// Expected preimages and signatures were made by an independent Python +
// OpenSSL process. There is no fixture signing key and Go does not sign here.
func TestIndependentReceiptPreimages(t *testing.T) {
	f := candidateReceipts(t)
	for i, v := range f.Vectors {
		t.Run(v.Case, func(t *testing.T) {
			ctx, headers, body := receiptInput(t, f, i)
			preimage := receiptHex(t, v.PreimageHex)
			hash := sha256.Sum256(body)
			sig, err := base64.RawURLEncoding.DecodeString(v.Signature)
			if err != nil || fmt.Sprintf("%x", hash) != v.BodySHA256 || len(preimage) < len(hash) ||
				!bytes.Equal(preimage[len(preimage)-len(hash):], hash[:]) || !ed25519.Verify(ctx.PublicKey, preimage, sig) {
				t.Fatal("independent literal bytes/hash/signature not verified")
			}
			got, err := verifyProof(ctx, headers, body)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal("candidate verifier did not reproduce proof")
			}
			// Same logical JSON, different exact transmitted bytes (including LF).
			mutated := append(append([]byte(nil), body...), ' ')
			if got, err := verifyProof(ctx, headers, mutated); err == nil || got != nil {
				t.Fatal("byte mutation accepted")
			}
			ctx.Generation ^= 1
			if got, err := verifyProof(ctx, headers, body); err == nil || got != nil {
				t.Fatal("generation substitution accepted")
			}
		})
	}
}

func TestSampleAttemptUsesOneCurrentReceipt(t *testing.T) {
	f := candidateReceipts(t)
	ctx, headers, body := receiptInput(t, f, 1)
	proof := DiscoveryProof{target: "target", session: "session", agent: ctx.AgentID, engine: ctx.EngineID, epoch: ctx.Epoch, key: string(ctx.PublicKey), generation: ctx.Generation, valid: true} // Test-only matching receipt; KAT generations differ.
	a := NewSampleAttempt(ctx, proof, "target", "session", true)
	v, err := a.Consume(200, headers, body)
	if err != nil || v.HasHost || !v.HasOwnedCount || v.OwnedCount != 1 || !v.HasOwnedUsage || v.OwnedUsage != 0 {
		t.Fatal("verified measured zero was not projected exactly")
	}
	if v, err := a.Consume(200, headers, body); err == nil || v != (Sample{}) {
		t.Fatal("duplicate completion accepted")
	}

	t.Run("stale slot", func(t *testing.T) {
		a := NewSampleAttempt(ctx, proof, "target", "session", true)
		a.Invalidate()
		if v, err := a.Consume(200, headers, body); err == nil || v != (Sample{}) {
			t.Fatal("invalidated completion accepted")
		}
		current, _, _ := receiptInput(t, f, 2)
		if v, err := NewSampleAttempt(current, proof, "target", "session", true).Consume(200, headers, body); err == nil || v != (Sample{}) {
			t.Fatal("old response accepted for newer request")
		}
	})
	t.Run("zero/default and no entitlement", func(t *testing.T) {
		var empty SampleAttempt
		if v, err := empty.Consume(200, headers, body); err == nil || v != (Sample{}) {
			t.Fatal("zero slot accepted reply")
		}
		noEntitlement := ctx
		noEntitlement.CallerAuthorized = false
		if v, err := NewSampleAttempt(noEntitlement, proof, "target", "session", true).Consume(200, headers, body); err == nil || v != (Sample{}) {
			t.Fatal("possession or valid signature implied permission")
		}
	})
	t.Run("duplicate or folded proof and wrong status", func(t *testing.T) {
		for _, tc := range []struct {
			status  int
			headers []string
		}{
			{200, []string{headers[0], headers[0]}},
			{200, []string{headers[0] + "," + headers[0]}},
			{503, headers},
		} {
			a := NewSampleAttempt(ctx, proof, "target", "session", true)
			if v, err := a.Consume(tc.status, tc.headers, body); err == nil || v != (Sample{}) {
				t.Fatal("ambiguous proof or non-success accepted")
			}
			if v, err := a.Consume(200, headers, body); err == nil || v != (Sample{}) {
				t.Fatal("bad completion did not consume slot")
			}
		}
	})
	t.Run("concurrent duplicates", func(t *testing.T) {
		a := NewSampleAttempt(ctx, proof, "target", "session", true)
		var accepted atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, err := a.Consume(200, headers, body); err == nil {
					accepted.Add(1)
				}
			})
		}
		wg.Wait()
		if accepted.Load() != 1 {
			t.Fatal("receipt slot must be consumed once")
		}
	})
}
