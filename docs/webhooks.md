# Webhooks

Brine owns event delivery and automation admission; Dill owns planning and execution. The current Dockerfile bundles Dill **0.13.0** with Pkl **0.31.1**. An accepted request, a delivered notification, and a successful execution are three independent facts.

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

Lifecycle notifications are journaled on the existing local UI/API runner paths as well as incoming-trigger execution. Git synchronization uses `git.sync.started`, `git.sync.completed`, `git.sync.failed` (a definite admission failure), and `git.sync.outcome_unknown` (the source may have changed but confirmation is unavailable). The explicit unknown event extends the original proposal so subscribers never receive a false failure signal. Successful sync events retain the reported `changes_detected` boolean, including false; uncertain results leave it unavailable rather than inventing no change.

Stack registration commits produce `stack.created`, `stack.updated`, and `stack.deleted`. These describe committed metadata, not every attempted or partially failed filesystem write. Retained source-lineage commits additionally produce `source.revision_created`; a reviewed workspace source exchange produces `source.restored`. Unreferenced archive objects and staging directories are not revisions or stack registrations. Source-only events have Target kind `none` and never imply an engine action.

The private `meta.json` event head closes the metadata-commit-to-outbox crash window and preserves successive rapid changes. Recovery requires a readable matching stack incarnation and exact commit marker, or confirmed registration absence under an intact inventory root for deletion. Missing inventory roots, unreadable metadata, and unresolved Restore fences remain unconfirmed; they never become successful or deleted events. Restore cleanup uncertainty retains the existing source recovery fence rather than publishing success prematurely. Recovered events set `reconciled: true` without replacing the original source-only reason. Terminal internal journal rows can be reclaimed after immutable event fanout; delivery/deduplication history keeps its own retention. External filesystem writers and disabled capture are not an audit source.

Server-managed, reviewed target registry commits produce `target.enrolled`, `target.updated`, and `target.removed`. These contain only a namespaced logical-record ID and operator alias, not addresses, key paths, pairing credentials, or whole records. They attest registration changes, **not** connectivity, capability availability, physical daemon identity, or execution. Their target kind is `registered-target`; stack-scoped subscriptions do not match these global events.

The registry's private optional `event_head` is committed in canonical `targets.json` without changing record revisions or approval comparisons. Recovery is bound to the configured registry directory and validated lock inode, not merely its alias. Both recovery and predecessor acknowledgement require directory durability. Missing/replaced registry state cannot confirm old intents. Older binaries reject the new private envelope field rather than silently treating it as a legacy authorization fallback. Offline/external registry writers without the configured observer are not an audit source.

When local runtime observation is configured, Brine samples its existing read-only engine adapter every five seconds (ten-second overall budget, five-second engine-read budget, at most 256 containers). This server-side notification sampling is independent of browser Live Updates. `container.state_changed`, `container.health_changed`, `stack.health_changed`, and local `target.connectivity_changed` describe accepted observations, not engine commands or exhaustive engine-event history. First sight establishes a baseline; no synthetic initial change is sent. Snapshot advancement and transition journaling are atomic, so delivery backpressure never advances a baseline without durable notification intent.

Observation payloads have `previous`/`current` availability plus nullable reported values. Reported empty strings remain reported empty; failed reads and missing membership become unavailable, never stopped, removed, or healthy. Docker health uses structured inspection fields, not status text or health-command output. Explicitly absent/disabled health checks are `not_configured`. Aggregate health requires observed local membership, every container running, and every health check reported healthy/starting/unhealthy; unchecked, unknown, inconsistent, or partial membership cannot produce healthy. Fully observed unhealthy takes precedence over starting. A workspace alone does not establish a local execution target; remote-owned and unresolved manual deployments are excluded from local stack association. Stack association uses managed runtime labels and does not attest a deployed source revision.

Baselines and pending transition intents are bounded separately. Inactive unavailable baselines expire under retention without dropping committed transition intents; retained active baselines refresh. Capacity exhaustion is explicit. Sampling is joined on shutdown; its running/unavailable/stopped/disabled diagnostic is separate from delivery and trigger workers and never attests workload health. Registration events do not supply runtime or capability evidence.

Remote observation uses the existing verified registry read-lease provider, never a local fallback. Two targets are attempted per pass in round-robin order, with bounded per-target contexts, complete bounded inventory, and coherent ID/stack/service/state inspections. Leases are released before event persistence. Runtime-only stacks have a separate target-namespaced `runtime-stack` identity, not a guessed Workspace/Git source. A registered Git source is attached only when its freshly read owner binding matches the exact verified leased HTTPS binding. Per-container inspection failures make health unavailable without discarding valid inventory siblings; failed inventory/session/cleanup invalidates previous readings.

The new agent `GET /v1/capabilities` document separates monitor methods from configured deployment grants. Values are retained as a sorted JSON-array string with `monitor.` / `deployment.` prefixes. Advertised deployment entries do not authorize a monitor credential to execute. Dill **0.13.0** exposes this endpoint; older agents such as 0.12.0 report capability-unavailable. Updating Brine's bundle does not update a remote agent or grant its monitor credential deployment access. Legacy SSH sessions without the report remain unavailable, never an empty capability set.

## Separate remote deployment credentials

Monitor pairing is read-only. Remote staging, manifest, preview, apply, fencing, removal and operation status require a distinct canonical 32-byte base64url deployment token plus the matching explicit host grants. Neither webhook signing keys nor monitor credentials substitute. Missing custody configuration denies before dispatch.

The operator supplies `--remote-deploy-credentials /absolute/private/index.json`. Both index and token files must be outside Brine's data/source directory, regular owner-only 0600 single-link files in owner-only 0700 directories with safe ACLs and trusted ancestry. Root-owned sticky temporary ancestors are permitted; bind mounts and privileged/same-UID writers remain trusted. The source does not write credentials. Obtain non-secret current enrollment metadata using `bin/brine --remote-registry-dir /absolute/private/registry --remote-deploy-binding TARGET_ID` (offline read only), then provision:

```json
{"version":1,"targets":{"TARGET_ID":{"binding":"EXACT_64_HEX_BINDING","enrollment":"EXACT_32_HEX_ENROLLMENT","token_file":"/absolute/private/deploy-token"}}}
```

The index is approved at startup and its exact digest is rechecked on every lookup; changing/removing it invalidates existing authority until restart. Tokens are re-read for each connection/request check; held clients never switch credentials. Rotating/removing token bytes prevents later requests and invalidates retained confirmations. Already dispatched operations may finish and must not be retried blindly. Restoring exact old bytes is not permanent revocation: retire the source/agent grant or restart to retire old clients. Forget/re-pair changes enrollment and cannot inherit an old index entry, even with the same pairing bundle. Reviewed apply/removal is additionally bound to the exact installed credential revision. Existing narrow operator-authority clients retain their explicit per-request allowlist; there is no status/removal/fence privilege inheritance.

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

### Remote lifecycle admission (Dill 0.13.0 paired protocol)

Remote lifecycle bindings currently require an enabled, successfully deployed, reviewed remote Git owner, an exact active enrollment and separate deployment custody. Protected/attended-source workflows and unproven historical owners remain unavailable, not legacy fallbacks. Brine retains the last verified deployment request and journal in the Git policy; lifecycle preview must name that predecessor. A same-name deployment replaced outside Brine cannot inherit the trigger. Enrollment, credential or source changes invalidate the immutable ceiling. Trigger creation remains authenticated and CSRF-protected; execution creates no browser identity.

The dispatcher shares the remote manual/Git admission lock and recovery ledger. It writes the immutable lifecycle recovery intent before dispatch, advances the verified deployment head only after a known successful response, and keeps uncertain/partial work blocking other callers. Target outcomes remain reported even if local recovery persistence fails. Authenticated inspection can reconcile a later known terminal result against the saved predecessor; it never replays the operation. Lifecycle recovery retains at most 128 resolved historical rows plus the newest intent; unresolved and existing manual deployment evidence are never evicted. Authoritative incoming deduplication and target replay protection remain in their separate durable journals.

For the same proven Git owner, `plan` captures the explicitly reviewed branch, stages it and requests Dill preflight without apply. An update preview must identify the verified deployment predecessor and journal. `git-sync` is source-only: it fast-forwards the workspace from the explicitly reviewed URL/branch, pins its own Git metadata, rejects URL rewrites, dirty/wrong-branch work and ambient Git-directory overrides, and never calls Dill. Independently approved background Git deployment remains a separate operation. Before the pull, Brine durably blocks the shared policy; an uncertain pull leaves `source-sync-unconfirmed` blocking all remote actions. Inspect the source, then use the authenticated Git settings source-inspection acknowledgement to disable automation without retrying it. A plain disable is refused while source sync is uncertain. The unknown request remains unknown; re-enabling trusted Git deployment requires its own explicit review. Never fence an engine request to pretend a source mutation did not occur. Successful synchronization preserves reported changes, including false; target runtime/deployment head is unchanged by these source actions.

For the same explicitly trusted automatic Git policy, incoming `deploy` reuses the existing captured-commit deployment runner and its durable attempted-revision/pending journal. It does not approve attended source workflows. A distinct `checked-apply` host grant and atomic exact-predecessor submission are required; there is no create or ordinary apply fallback. Pending/unknown work is reconciled by status lookup, never resubmitted. Previously attempted revisions are refused with no execution, including branch rollback; they are not reported as a successful new deployment. A known target outcome remains visible if saving local recovery state fails.

Ordinary remote UI stage/preflight, reviewed apply/removal, and automatic Git preflight/apply use passive durable operation observations around the existing SDK calls. Observation grants no authority and never resubmits. A journal admission failure prevents a new send; target results remain unchanged if recording their outcome fails. Reported completion must match the exact request and stack. Runtime-only removal/preflight cannot acquire a local source association merely through a same-name workspace. Incoming actions retain their own single operation journal. Later status inspection remains read-only, not another execution.

The published Dill 0.13.0 binaries and schema have been verified with the six-action disposable Linux Docker/browser flow. Additional protected operator qualification, final integration review and Brine CI/merge remain release-gate work; this branch is not a completed release.

The existing central-secret operator controller now journals its admitted Plan and reviewed apply boundaries as well. Preparation/preview is not deployment; Plan never reports runtime effects. Observer metadata contains logical stack/target/action/request facts only, never values, binding references, provider locators or source bytes. Target replies must match the exact request and stack. A later local history-write error retains recovery blocking without downgrading a validated target result. Status/reconciliation creates no second execution event and never resubmits. These hooks add no incoming authority; protected broker registrations remain unavailable to unattended triggers. Actual protected-provider/browser qualification of this boundary remains release-gate work.

History and deduplication are bounded by configured retention. Unresolved unknown/awaiting-review incoming records are retained until inspection. Queue or journal exhaustion refuses new work explicitly rather than dropping it or inventing successful delivery.

## Verification boundaries

See [source/target support and evidence limits](webhooks-support.md). Retained remote ownership evidence stays excluded from local incoming execution after policy disable or request inspection; neither is an explicit local target handoff.

The disposable `brine-fixture --synthetic-webhooks` mode exercises production management handlers and ephemeral storage. It deliberately does not start delivery workers, send network requests, or run Dill. Its pending test deliveries are real queue records, not fabricated receiver acknowledgements. Browser checks against that fixture are UI verification, not end-to-end execution qualification.
