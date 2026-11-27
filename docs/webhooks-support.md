# Webhook source and execution boundaries

This is an implementation and evidence matrix, **not a release verdict**. The
Dockerfile bundles released Dill **0.13.0** with Pkl **0.31.1**. Verified published
binaries/schema pass managed Linux Docker, HTTPS SDK and the signed all-six-action
browser flow. Protected broker producer/operator qualification, final review and
Brine CI/merge remain release work, not phased support.

## Incoming actions

| Source / actual execution target | Boundary | Evidence |
|---|---|---|
| Ordinary local Workspace / Docker or Podman | Plan, deploy, start, stop, restart use existing runner admission/locks. Workspace has no Git sync. Explicit engine/socket and matching declared engine required. | Local handler/runner component checks; Docker/Podman admission matrix below is **not** real-engine qualification. |
| Ordinary local root-level Git / local engine | Same actions plus source-only Git sync through existing pull/status path. Git source alone does not imply remote execution. | `webhook_git_test.go`; synthetic Git repository and runner component checks. |
| Local source needing deployment staging | Deploy records awaiting review and sends no operation; no unattended review is fabricated. | `TestDispatcherSupportsLocalActionsAndKeepsReviewBoundary`. |
| Local retired/strict/qualified execution paths, missing runner gates, unreadable state, secret/protected denial | No legacy incoming fallback. Source history without a source-protection check denies; a configured check must independently allow the source. | `webhook_dispatch.go: Validate`; denial tests. |
| Source with retained remote Git policy, including disabled policy | Never offered local incoming execution. Disabling remote automation does not move the source to a local engine. | `TestDisabledRemoteGitNeverBecomesLocalWebhookGrant`. |
| Source with remote manual request evidence, including resolved inspection | Never offered local incoming execution. Inspection is not target handoff. The ledger cannot prove local authority, even if an operation was fenced without effects. No implicit handoff/record deletion is introduced. | `TestLocalWebhookNeverReclaimsInspectedRemoteSource`. |
| Root-level explicitly trusted Git / current enrolled paired target | All six actions only after a verified successful deployment anchors request and journal, fresh explicit grants, source/enrollment/custody binding and clear shared recovery. Plan and Git sync additionally require read-only `stop-preview` plus `journal`: live retained ownership is checked before capture/staging or pull. No stop execution is submitted. Plan and Git sync never apply; deploy uses exact-predecessor checked apply. | Real Chrome branch authorization, ordinary first deployment, signed all-six actions and actual Linux Docker recreation in `TestSignedIncomingRemoteLifecycle`. |
| Remote manual Workspace / paired target | Existing interactive captured-file review, transfer/preflight and explicit create/update/removal confirmation. It does **not** establish unattended incoming authority. | Actual Chrome workspace create/update/recreation and exact request inspection; removal observer also covered by handler component tests. |
| Remote source with history/protection/attended authority, Git subfolder, absent anchors, old/incomplete capabilities, changed custody/enrollment, outside same-name replacement | No incoming option/fallback. An unmounted attended adapter or one-shot preview is not an automation grant. | `lifecycleSource`, `LifecycleTargets`, `RunSourceAction`, shared Git runner and denial tests; outside replacement also real signed pair. |

`WebhookOptions` is discovery, not submission authority. Validation is repeated
under the actual operation's admission boundary. Unsupported/unavailable is not
an empty successful target response. Local socket identity is not physical
attestation; root/same-UID/mount topology remain trusted.

## Outgoing transport qualification

`TestRealHTTPSDeliveryPreservesUnknownAndHistory` is an opt-in disposable Linux
check using production service initialization and sender workers against a generated
TLS receiver on a private, non-loopback interface. Default trust rejects the
untrusted certificate before HTTP; fixture-only trust then permits HMAC-verified
delivery and explicit redelivery through the unchanged destination policy. Three
attempts and stable delivery identity persist, the reported operation remains
unknown, and receiver response bodies are not retained. It needs neither an engine
socket nor published host ports. This is transport evidence, not a substitute for
producer/catalog and operator qualification.

## Restore and uncertainty

- Source Restore is a separate authenticated browser-only, private local archive
  operation for eligible Workspace sources. No Git checkout overwrite, remote
  fallback, deployment from a receipt, push, variables, secret stores or runtime
  data restoration. Real CLI/Chrome qualification verifies checkpoint, retained
  previous files, a new restored revision, offline exact archive lineage and
  unchanged engine state; it does not qualify protected remote execution.
- Protected local source: a real CLI/browser fixture provisions monotonic
  protection metadata offline with the production writer, then proves history
  controls unavailable, authenticated checkpoint review denied, and webhook
  options exclude it while still including an eligible sibling. Source bytes,
  zero retained revisions, unchanged runtime and zero lifecycle records are
  checked. After shutdown, loss of the protection-state root refuses real CLI
  startup without recreating it. This is denial/recovery-boundary evidence,
  **not** a secret-deployment setup or protected execution qualification.
- Lost successful HTTPS stop response: real target stopped, Brine records unknown,
  holds the binding and never replays. Exact target inspection advances verified
  lineage; a separate browser acknowledgement resolves the queue while preserving
  its reported unknown outcome. A new request may then execute.
- Engine start outage after replacement: this tested pair reports **unknown /
  unknown**, while observed runtime has lost the original and cleaned failed
  replacement. Inspection leaves recovery unresolved. A held valid deployment
  review is denied by recovery admission. Do not call this a reported
  failed/partial receipt or infer success/failure from the observed damage.
- `TestIncomingWorkerHardCrashDoesNotReplay` kills an actual queue worker process
  after durable `running` plus readiness sent after marker sync/close. Both
  reopened workers complete fresh independent-binding probes, while the crashed
  request keeps stable unknown/duplicate identity/binding block and never repeats. This is a
  **queue-component** crash check, not a whole Brine/target/browser crash test.

## Engine evidence limits

Real new paired managed execution has been qualified on a dedicated **native
Linux Docker** daemon only. The underlying named managed contract is not a claim
of Podman support. `TestLocalEngineAdmissionMatrixIsNotRuntimeQualification`
checks both local engine selections with a synthetic runner; it must not be cited
as a Podman runtime pass. Ordinary Dill Docker and Podman integration CI passed on the exact reviewed and
merged protocol source. That does not qualify the named managed protocol on
Podman. Remaining protected/operator matrix, final review and required Brine
CI/merge checks remain open.

Current real-pair harness inputs and exact commands are retained in the delivery
record. Native opt-in tests skip without explicit disposable runtime variables;
a native `make test` pass alone does not establish real-engine/browser evidence.
