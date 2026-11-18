# Direct remote agent: first transport slice

This branch adds a pre-enrolled HTTPS read transport. Dill can now generate and export pairing material locally; this Brine branch does not yet provide the operator-facing pairing UI or deployment. The existing SSH target registry and UI remain unchanged.

- One direct private IP and port per host; no proxy or redirect following. The initial transport accepts RFC 1918, IPv6 unique-local and loopback literals. It does not expose a Docker TCP endpoint.
- Dill serves TLS 1.3 with an operator-provisioned certificate/key and requires a per-agent bearer credential on every supported request. Brine checks the independently supplied SHA-256 pin of the leaf certificate DER and the certificate validity period before sending that credential. The token is the unpadded base64url encoding of 32 random bytes. Keep the token, private key and their directory paths private. Replacing the pinned certificate requires enrolling the new pin; changing the token requires updating both ends and restarting the agent. There is no online discovery or trust on first use.
- The HTTPS allowlist is `/v1/session`, `/v1/inventory`, `/v1/inspect`, and `/v1/logs`. The read protocol still verifies the enrolled signed agent and engine binding. HTTPS reveal, uploads and all deploy/remove paths are excluded from this slice.

Dill's `init-https` generates stable credentials and `--print-https-pairing` explicitly exports a reusable secret bundle. `DILL_AGENT_HTTPS_BIN=/absolute/dill-agent go test ./internal/agentclient -run '^TestHTTPS'` covers the built Dill process and Brine client over loopback against a synthetic Docker Unix socket. It is not a real-host or Brine application qualification.

Next: add authenticated Brine pairing, private credential storage and revocation; verify the actual application together. Add deployment separately behind a host-local setting, with snapshot review, target-local preflight and shared CLI locking, durable outcome lookup, and managed-only removal that retains data. Brine, never the agent, watches Git and submits exact-commit snapshots through the same deployment path. Unknown write outcomes are not retried.
