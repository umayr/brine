# Webhooks

Brine owns event delivery and automation admission; Dill owns planning and execution. The current Dockerfile bundles Dill **0.11.0**. An accepted request, a delivered notification, and a successful execution are three independent facts.

## Enablement and key custody

Webhooks are off by default. First provision the key, then initialize a new, empty state directory with `--init-webhooks` plus all the settings below (and your explicit `--data-dir`). This offline command exits without contacting an engine or starting HTTP. It refuses existing state. Normal startup only opens initialized state: a missing database or lock is an error, never an empty installation.

Configure all of:

- `--webhooks-enabled`
- `--webhooks-dir /absolute/private/state`
- `--webhooks-key-file /absolute/separate/keys/webhooks.key`

Provision the encryption key independently: exactly **32 raw random bytes**, not 64 hexadecimal characters. Its regular file must be owned by Brine's effective user, readable by that user only (0400 or 0600), and have one link. Symlink and hardlink keys are rejected. Keep the key outside Brine's data directory and the webhook state directory, including through parent-directory aliases. Startup neither generates nor repairs keys. Back up state and key separately; losing the key makes stored signing secrets unrecoverable. Replacing an existing encryption key is not signing-secret rotation and will fail startup.

Explicit initialization creates the state directory privately if absent; an existing directory must be owner-only 0700. Its SQLite database and sidecars must remain private regular files. Only one Brine process may hold this state directory at a time. Never place webhook state in stack source or Git repositories. Key directories and mount topology are trusted operator configuration: do not bind-mount or otherwise expose the key subtree beneath stack data. Brine resolves parent-directory symlinks but cannot protect against a privileged operator copying or remounting key material into source. Webhooks cannot be enabled with `--no-auth`.

Optional restart-only settings:

| Flag | Default | Limits |
|---|---|---|
| `--webhooks-max-pending` | 10000 | 1–100000; 0 selects default |
| `--webhooks-retention` | 168h | 24h–2160h; 0 selects default |
| `--webhooks-allow-private-cidr` | None | Repeatable, explicit private receiver subnets |

Private receivers require an operator CIDR allowlist. This never enables loopback, link-local, metadata services, multicast, or IPv6 transition-address bypasses. HTTPS is mandatory; URL credentials, query parameters, fragments, redirects, and environment HTTP proxies are not supported. DNS answers are checked and the dial is pinned to a permitted numeric address while TLS verifies the original hostname.

## Outgoing requests

Requests are JSON POSTs with:

- `Brine-Event-ID`: stable event identity.
- `Brine-Delivery-ID`: delivery identity; attempts are recorded separately.
- `Brine-Timestamp`: canonical Unix seconds.
- `Brine-Signature`: `v1=` followed by lowercase hexadecimal HMAC-SHA256.

The signed bytes are `timestamp + "." + raw_request_body`. The HMAC key is the UTF-8 text of the one-time signing secret shown by Settings. **Do not hex-decode that signing secret.** It is distinct from the raw 32-byte at-rest encryption key.

Receivers should authenticate the raw bytes before decoding, enforce a five-minute timestamp window, and deduplicate by event ID. Delivery is at least once, not globally ordered or exactly once. A timeout can mean the receiver processed the event but its acknowledgement was lost.

Only HTTP 2xx marks delivery complete. Network errors, 408, 429, and 5xx receive bounded backoff and jitter; `Retry-After` is bounded. Automatic attempts stop after eight attempts or 24 hours. Attempts are reserved durably before sending, so crashes cannot evade the budget. Other responses require operator intervention. Receiver response bodies and raw network errors are not stored in history.

Pausing retains the existing backlog and stops queuing new matching events. Changing the destination generates a new signing key and cancels old pending deliveries; queued payloads never move to another receiver. Rotating the signing secret also cancels pending old-key deliveries. Already admitted in-flight requests may finish. Redelivery is explicit, preserves event identity and attempt history, and never reruns a lifecycle operation. Retired destinations or credentials cannot be redelivered.

Event payloads carry allowlisted metadata, with Source independent of Target. They do not contain source contents, environment values, credentials, raw logs, or receiver bodies. Unknown and unavailable are not successful outcomes. A source Restore is not a deployment, volume restore, or secret restore.

## Incoming requests

A trigger grants one action on one immutable stack/target binding. It uses its own signing secret, not Brine's general API token. POST JSON to the exact `/hooks/{trigger_id}` URL:

```json
{"id":"your-stable-unique-request-id"}
```

Supply `Content-Type: application/json`, `Brine-Timestamp`, and `Brine-Signature`, using the same signing algorithm above. Maximum body size is 4096 bytes. No action, target override, source path, shell command, variable, or secret value can be supplied in the request body. Query-string authentication is not supported.

A valid request returns HTTP 202 with its durable acceptance record. Repeat the identical body, with a fresh timestamp/signature, to retrieve that request's current record without executing it again. Reusing the ID with different bytes or a changed grant returns a conflict. Disabled, expired, revoked, or rebound credentials do not authorize new requests. A changed stack/action/target generates a new key and invalidates queued grants.

Admission is checked again immediately before execution. Existing source protection, ownership, target capability, and review rules still apply. An incoming request never fabricates a browser session or approves a reviewed operation. Available action/binding choices are supplied by the dispatcher; an unavailable option is not an execution fallback.

One active or uncertain request occupies a stack/target binding. After a crash, an executing request becomes **unknown** and is never automatically replayed. Unknown and awaiting-review records keep the binding blocked until an authenticated operator reviews the exact request and independently inspects the target. The inspection acknowledgement releases Brine's block only; it does not change the reported outcome, retry, approve, deploy, or restore anything.

## Management and diagnostics

Settings → Webhooks manages endpoints and triggers, displays delivery/request history, and offers a synthetic `webhook.test` connectivity event. Test acceptance is not successful delivery, and a successful delivery test does not verify lifecycle integration.

Configuration changes and secret display require a browser session and CSRF protection. Authenticated API reads do not expose signing keys. Keys are shown only once after creation/rebinding/rotation and are cleared from the page when dismissed. Worker liveness is reported separately; a stopped worker does not imply an empty queue or a failed deployment.

History and deduplication are bounded by configured retention. Unresolved unknown/awaiting-review incoming records are retained until inspection. Queue or journal exhaustion refuses new work explicitly rather than dropping it or inventing successful delivery.

## Verification boundaries

The disposable `brine-fixture --synthetic-webhooks` mode exercises production management handlers and ephemeral storage. It deliberately does not start delivery workers, send network requests, or run Dill. Its pending test deliveries are real queue records, not fabricated receiver acknowledgements. Browser checks against that fixture are UI verification, not end-to-end execution qualification.
