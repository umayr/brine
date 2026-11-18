# Direct remote agent: first transport slice

This branch adds a pre-enrolled HTTPS read transport, not an operator-facing pairing or deployment workflow. Brine's existing SSH target registry and UI remain unchanged.

- One direct private IP and port per host; no proxy or redirect following. The initial transport accepts RFC 1918, IPv6 unique-local and loopback literals. It does not expose a Docker TCP endpoint.
- Dill serves TLS 1.3 with an operator-provisioned certificate/key and requires a per-agent bearer credential on every supported request. Brine checks the independently supplied SHA-256 pin of the leaf certificate DER and the certificate validity period before sending that credential. The token is the unpadded base64url encoding of 32 random bytes. Keep the token, private key and their directory paths private. Replacing the pinned certificate requires enrolling the new pin; changing the token requires updating both ends and restarting the agent. There is no online discovery or trust on first use.
- The HTTPS allowlist is `/v1/session`, `/v1/inventory`, `/v1/inspect`, and `/v1/logs`. The read protocol still verifies the enrolled signed agent and engine binding. HTTPS reveal, uploads and all deploy/remove paths are excluded from this slice.

Next: generate stable identity and a one-time manual pairing bundle on the host; add authenticated Brine pairing, private credential storage and revocation; verify the actual binaries together. Add deployment separately behind a host-local setting, with snapshot review, target-local preflight and shared CLI locking, durable outcome lookup, and managed-only removal that retains data. Brine, never the agent, watches Git and submits exact-commit snapshots through the same deployment path. Unknown write outcomes are not retried.
