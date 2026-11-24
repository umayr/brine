package remoteui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
)

var errOperatorAuthority = errors.New("operator-configured deployment authority unavailable")

// operatorGrantApproval contains only locally reviewed configuration. Neither
// the allowlist nor its revision is an attestation from Dill. The opaque token
// reference is resolved by trusted infrastructure, not by a form or a URL.
type operatorGrantApproval struct {
	Enrollment          targetregistry.HistoryIncarnation
	Target              agentclient.OperatorTarget
	LocalGrants         agentclient.LocalDeployGrants
	Reference, Revision string
	// Digest of the canonical high-entropy token at review, never archived.
	TokenCommitment [32]byte
}

// operatorGrantSource returns the currently approved metadata and canonical
// bearer for a lease-bound active enrollment. A production source must restrict
// access to approved operators, refuse untrusted paths/backends, and advance or
// revoke the revision before material or policy changes. No source is installed
// by this slice. Errors are intentionally not propagated to requests or logs.
type operatorGrantSource interface {
	LoadCurrent(context.Context, targetregistry.HistoryIncarnation) (operatorGrantApproval, string, error)
}

type operatorDeployAuthority struct{ source operatorGrantSource }

func (a *operatorDeployAuthority) connect(ctx context.Context, record targetregistry.Record, active targetregistry.HistoryIncarnation) (attendedConnection, error) {
	if a == nil || a.source == nil || ctx == nil || ctx.Err() != nil || active == (targetregistry.HistoryIncarnation{}) {
		return attendedConnection{}, errOperatorAuthority
	}
	approved, bearer, err := a.source.LoadCurrent(ctx, active)
	if err != nil || approved.Enrollment != active || !attendedHex(approved.Revision, 32) || !attendedHex(approved.Reference, 32) || approved.LocalGrants != agentclient.LocalNewAttended || sha256.Sum256([]byte(bearer)) != approved.TokenCommitment {
		return attendedConnection{}, errOperatorAuthority
	}
	pair, err := agentclient.ParsePairing([]byte(record.HTTPSPairing))
	if err != nil || !pair.MatchesOperatorTarget(approved.Target, record.HTTPSAddress) {
		return attendedConnection{}, errOperatorAuthority
	}
	current := func(ctx context.Context) bool {
		if ctx.Err() != nil {
			return false
		}
		latest, token, err := a.source.LoadCurrent(ctx, active)
		// Never switch a live transport to a different revision, reference, or
		// bytes. In particular ambient replacement does not refresh authority.
		return err == nil && latest == approved && subtle.ConstantTimeCompare([]byte(token), []byte(bearer)) == 1 && ctx.Err() == nil
	}
	encoded, err := json.Marshal(approved)
	if err != nil {
		return attendedConnection{}, errOperatorAuthority
	}
	revision := sha256.Sum256(encoded)
	client, epoch, err := pair.OpenOperatorDeploy(ctx, approved.Target, bearer, approved.LocalGrants, current)
	if err != nil {
		return attendedConnection{}, errOperatorAuthority
	}
	return attendedConnection{client: client, epoch: epoch, binding: attendedBinding{
		target: targetDigest(record), scope: hex.EncodeToString(active[:]), revision: hex.EncodeToString(revision[:]),
		contractVersion: "dill/0.9.0", distinct: true, grants: attendedGrants,
	}}, nil
}
