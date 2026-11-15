# Paired Alpha application canary: receiving boundary

This document records the reviewed attended **component** interface and the
remaining receiving contracts. It does not freeze an external proof format,
authorize a driver, or qualify the Brine application. The component remains
dormant: production startup does not provide its real source/evaluator factory
or activate its runtime gate. The original Pkl runtime audit is **deferred, not
waived**. Actual source and paired-app qualification remain off.

## Reviewed component and evidence boundary

Brine PR #81 was normally merged as
`2f0dea818b637531a77c730fc08678f46c55186b`, from reviewed head
`794e36d77a1c6ab97b9afdf451fa6b544a2346bc`. All six exact-head PR checks
succeeded: CI run `43679129272` and contract run `43679129274`, including
strict no-skip Linux synthetic job `132705720822` with real-store API and
authenticated real-API/store browser coverage. The browser uses a fixed
synthetic producer and deliberately unknown apply, **not** a native engine,
qualified source or successful application operation. Post-merge main CI runs
`43679951315` and `43679951464` both succeeded. The supervisor verified these
results; neither PR nor post-merge CI establishes source or application
qualification.

Component source of record includes `internal/api/attended_context.go`,
`internal/api/attended_controller.go`, `internal/api/attended_history.go`,
`internal/api/operations.go`, `internal/operationhistory/history_v2.go`,
`internal/operationhistory/lookup.go`, `internal/web/static/attended.js`, and
`docs/alpha-attended-flow.md`. This is a receiving description of those reviewed
component semantics, not an implementation or permission to invoke them.

## Attended request and lookup contract

* Authenticated `POST /api/stacks/{name}/operations/preview` accepts a closed
  JSON object containing exactly `action`, one of `up`, `start`, `restart`,
  `stop`, or `down`. It does not accept caller-selected flags, services, source,
  target, actor, path or digests. The body is bounded at 4096 bytes; duplicate,
  unknown, missing, non-string, trailing and oversized input is rejected.
* A complete, durably stored preview returns `requestId`, `reviewDigest`,
  `confirmationToken`, and `review`. The review is a V2 history record, not a
  source proof or predicted apply success. When an ID is available,
  `X-Brine-Request-ID` and `Location: /api/operations/{id}` identify the Brine
  preview request, not a Dill operation.
* Confirmation uses the existing streaming
  `POST /api/stacks/{name}/{up,start,restart,stop,down}` with exactly
  `requestId`, `reviewDigest`, and `confirmationToken`: all are required strings
  of at most 1024 bytes, within a 4096-byte body. These literal names are not
  interchangeable with generic preview-reference or context-digest labels.
  JSON parsing occurs before token lookup: a malformed DTO is rejected with 400
  without consuming a valid token. Once found, a recognized in-memory token is
  consumed **before** expiry, identity and mismatch checks, revalidation and
  durable apply reservation. A consumed token cannot be retried; obtain a fresh
  preview. Tokens are not reconstructed from history.
* An apply gets a distinct Brine request ID in the same headers. `Started`
  precedes work and terminal history persistence precedes the terminal marker.
  HTTP 200, a flushed stream, disconnect or absent marker does not prove outcome
  or delivery. Process exit, invocation status, receipt outcome/effects, durable
  persistence and delivery are separate observations.
* Authenticated `GET /api/operations/{id}` returns the original typed V1 or V2
  record, not an invented wrapper. Lookup is observational; it does not mint a
  token, authorize replay or identify an unknown Dill operation. Invalid/missing
  IDs return 404 and unsafe history returns a safe failure. Protect history and
  all operational details as private.

The future startup integration must explicitly install **both** `api.MountAttended`
and `web.MountAttended` with the same Alpha context and local agent. It must set
Alpha before constructing the agent, use the same runner, retain the read-only
engine boundary, supply qualified-operation configuration and abort on constructor
failure. Neither mount installs the other. Existing authentication middleware is
required on both; cookie mutations retain CSRF checks. An unavailable producer
is configured-and-denied, never a legacy fallback. No per-request runner or
coordinator copies are permitted.

`AttendedProducer`/`AttendedCapture` are trusted startup dependencies, not an
HTTP source selector. Their required immutable export, capture, source/target
revalidation, request-specific resolver and original shared deployment boundary
are **assumptions until independently established**, not proof. SourceRef v2
contains snapshot/evaluator-profile/evaluator-reference digests; these are
producer assertions, not authority or proof of hermetic evaluation. Fixed
full-stack arguments and actual qualified source behavior require the separately
accepted producer contract.

## Six receiving contracts remain hard off

1. **Registry and authority:** field-specific accepted record kinds, producer,
   immutable identity, expiry/revocation, driver compatibility and external trust
   roots are not supplied. A digest, environment value or self-asserted status
   alone is not acceptance; final-driver compatibility must not manufacture or
   circularly determine upstream runtime acceptance.
2. **Closure and original runtime evidence:** the actual security-relevant Brine
   caller/transport/config, launcher and native Pkl build/closure map, and the
   complete original authorized positive/negative runtime evidence are missing.
   No substitute audit or filtered evidence is authorized.
3. **Canonical artifacts:** approved immutable driver plus imports/browser
   configuration and execution map, Brine source/server and actually served web
   bytes, and launcher archive versus executable provenance/hashes are not yet
   specified and accepted. Native Pkl and the Brine Pkl launcher are distinct.
4. **Case phase/fences:** exact source-qualified request/receipt semantics,
   external deterministic crash and contention fences, and a real supported
   target-mismatch transition are missing. Target configuration is startup-bound;
   token loss, an assumed live reload, a second daemon or a timing race is not a
   substitute. A Brine restart-unknown request does not imply a Dill ID, journal
   record or barrier; a child may never exist or may have durably completed.
5. **Independent observation and operator:** actual read-only event/inventory and
   named/foreign-data readers, measurement windows and finite known-ID operator
   protocol remain unprovided. Empty retained-network Down `failed/none` is not
   automatically an inspection barrier. A nonempty `failed/partial` case needs
   an actual validated APPLY ID and separately approved inspection/one exact-ID
   acknowledgement, not a guessed or historical ID. Up no-op may legitimately
   ensure started containers and network; no unintended recreate/pull/removal or
   state change is the relevant oracle, not zero delegate calls.
6. **Provisioner and outer collector:** trusted fresh native receiving-host
   provisioning, verification of all four exact executable artifacts before any
   candidate execution, independent daemon identity, private data boundaries,
   cleanup and an exact one-file public collector are not supplied. Missing,
   unrun, blocked, unknown or failed required cases must not become a pass.

## Dill input is not app/source qualification

The separately reviewed Dill input-v2 preparer and its source-owned 20-key pin
verify a particular accepted input artifact; they are not the Brine pair/source
registry, receiving-host authority or application/source qualification. Their
`linux-amd64` literal belongs to that input schema; it is not silently identical
to a different schema's `linux/amd64`. The historical provider receipt does not
supply a receiving daemon identity. Provider pin promotion is currently **held**
for the confirmed older `2d` private-host-IP `0.0.0.0`/`::` alias issue and
unknown native impact; do not silently promote a successor tuple. A bounded
corrected-provider qualification is authorized. Replacing the pin with a new
tuple requires actual acceptance by both sides; that acceptance is not asserted
here.

Any future actual canary must run only on a separately accepted disposable GitHub
Actions host after source, provider and receiving contracts are satisfied. This
component, its synthetic tests and this document do not complete those contracts.
