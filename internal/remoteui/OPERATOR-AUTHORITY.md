# Unmounted operator-configured authority for new deployments

Stock Dill v0.9.0 `d2636ecfe6d4b22f9055537b36468bd00cb12aad` keeps v1 signed
session/inventory under the monitor bearer. Its single deploy bearer is separate
and has host-local named grants. Brine's local allowlist is a ceiling, not Dill
attestation, status attenuation, or permission to skip confirmation. A valid
monitor session says nothing about the deploy bearer's provider grants.

The unmounted operator adapter takes an explicitly reviewed non-secret approval
from trusted operator infrastructure, matched to a held active enrollment, exact
origin/leaf pin and signed monitor agent/engine tuple. The approval includes an
opaque credential reference, local revision, reviewed operations and an in-memory
commitment to the reviewed canonical high-entropy credential. A current revision
check before each request refuses changed input, bytes or policy; it never swaps
a bearer on an existing client. Already dispatched requests cannot be revoked or
replayed. The caller's attended preview is tied to the effective local approval
revision, so a fresh transport under a different approved generation cannot use
that confirmation. Only stage, manifest, preview and journal-bound create/update
are permitted; no status, removal, fence or restore is authorized by this adapter.

Separate TLS transports use a fixed IP origin, exact leaf pin, no ambient proxy
or redirected requests and no monitor/deploy fallback. The deploy client never
performs a signed monitor session with the deploy bearer. It borrows only the
lifetime of the validated monitor proof. Terminal receipts still require the
existing exact journal echo and response checks. No secret or reference is
written to archive or logs. Only a non-secret derived approval revision is passed
to archive intent. Transport errors and denials do not establish non-admission.

There is no real credential source, startup/config mounting, HTTP activation,
credential provisioning or production qualification here. A future trusted
source must implement private reference resolution, operator-only approval and
revision/custody lifecycle. In particular, no ambient token-file reader may
silently bless replacement bytes or auto-rotate a local approval. Generated
loopback fixtures exercise the seam, not the released pair or live authority.
