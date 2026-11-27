package brokerwire

import (
	"testing"
	"time"
)

func TestPreparedReplyValidFor(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	scope := Scope{
		Registration:    "00112233445566778899aabbccddeeff",
		Stack:           "example",
		Engine:          "docker",
		TargetDigest:    Digest([]byte("target")),
		SourceDigest:    Digest([]byte("source")),
		ConfigDigest:    Digest([]byte("config")),
		EndpointDigest:  Digest([]byte("endpoint")),
		BindingRevision: 1,
		PolicyRevision:  Digest([]byte("policy")),
		Generation:      "ffeeddccbbaa99887766554433221100",
	}
	valid := PreparedReply{
		Contract: Version,
		Prepared: Review{
			Handle:    "0123456789abcdef0123456789abcdef",
			Scope:     scope,
			ExpiresAt: now.Add(time.Minute),
		},
	}
	tests := []struct {
		name   string
		change func(*PreparedReply)
		want   bool
	}{
		{"valid", nil, true},
		{"wrong contract version", func(r *PreparedReply) { r.Contract = "dill.broker-secrets/2" }, false},
		{"registration mismatch", func(r *PreparedReply) { r.Prepared.Scope.Registration = scope.Generation }, false},
		{"stack mismatch", func(r *PreparedReply) { r.Prepared.Scope.Stack = "other" }, false},
		{"engine mismatch", func(r *PreparedReply) { r.Prepared.Scope.Engine = "podman" }, false},
		{"target digest mismatch", func(r *PreparedReply) { r.Prepared.Scope.TargetDigest = scope.SourceDigest }, false},
		{"source digest mismatch", func(r *PreparedReply) { r.Prepared.Scope.SourceDigest = scope.ConfigDigest }, false},
		{"config digest mismatch", func(r *PreparedReply) { r.Prepared.Scope.ConfigDigest = scope.EndpointDigest }, false},
		{"endpoint digest mismatch", func(r *PreparedReply) { r.Prepared.Scope.EndpointDigest = scope.TargetDigest }, false},
		{"binding revision mismatch", func(r *PreparedReply) { r.Prepared.Scope.BindingRevision++ }, false},
		{"policy revision mismatch", func(r *PreparedReply) { r.Prepared.Scope.PolicyRevision = scope.TargetDigest }, false},
		{"generation mismatch", func(r *PreparedReply) { r.Prepared.Scope.Generation = scope.Registration }, false},
		{"active review", func(r *PreparedReply) { r.Prepared.Active = true }, false},
		{"empty handle", func(r *PreparedReply) { r.Prepared.Handle = "" }, false},
		{"uppercase handle", func(r *PreparedReply) { r.Prepared.Handle = "0123456789ABCDEF0123456789ABCDEF" }, false},
		{"non-hex handle", func(r *PreparedReply) { r.Prepared.Handle = "0123456789abcdef0123456789abcdeg" }, false},
		{"expired", func(r *PreparedReply) { r.Prepared.ExpiresAt = now.Add(-time.Nanosecond) }, false},
		{"expires at now", func(r *PreparedReply) { r.Prepared.ExpiresAt = now }, false},
		{"expires just after now", func(r *PreparedReply) { r.Prepared.ExpiresAt = now.Add(time.Nanosecond) }, true},
		{"expires at five minutes", func(r *PreparedReply) { r.Prepared.ExpiresAt = now.Add(5 * time.Minute) }, true},
		{"expires just after five minutes", func(r *PreparedReply) { r.Prepared.ExpiresAt = now.Add(5*time.Minute + time.Nanosecond) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := valid
			if tt.change != nil {
				tt.change(&reply)
			}
			if got := reply.ValidFor(scope, now); got != tt.want {
				t.Errorf("ValidFor() = %t, want %t", got, tt.want)
			}
		})
	}
}
