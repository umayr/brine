# Dormant attended API and synthetic fixture

> **Historical specification:** Alpha and strict-secret functionality is disabled in Brine. This document is retained for design and recovery context, not as operating instructions. Existing secret state and downgrade protections must not be removed or bypassed.
>
> **Current CLI:** Normal server startup rejects `--profile alpha` and explicitly supplied `--coordination-*`, `--operation-state-dir`, and `--secret-*` options, including empty values. Examples and operational claims below describe the historical contract, not currently available behavior.

This component is **not activated or qualified** by the application. The real source/evaluator factory, native application and runtime gates are separate and remain off. Digest-shaped source references are producer assertions, not verification labels or proof of hermetic output. Independent review and exact-head Linux CI remain required.

## Configuration boundary and guard pairing

The ordinary API/web mounts preserve legacy behavior. A future startup integration must explicitly construct `api.NewAttendedContext` with the **same** Alpha-profile runner installed in the local agent before `NewLocalAgent`, a qualified-operation configuration shape, and the trusted producer. Constructor failure must abort startup. The constructor verifies the `AlphaReadOnly` engine wrapper and shared runner identity; it does not prove native qualification, target identity, safe source capture, or runtime readiness. The producer may be nil or unavailable: this is configured-and-denied, never legacy opt-out. The only production history dependency is a concrete durable `operationhistory.Store`; the method-only fake used in tests is not a production fallback.

Startup must install **both** `api.MountAttended` and `web.MountAttended` with the same context and agent before serving requests; neither mount automatically installs the other. The current API guard denies all mutating routes except the preview and five confirmation endpoints, and denies legacy plan/validate and secret-binding reads. The web guard denies web mutations and legacy stack plan/container-evaluation endpoints. This conservative draft also denies source edits through those routes. These guards do not configure the Git syncer; the strictly Alpha-only contract relies on its existing Alpha pre-pull/pre-deploy guards. Do not install attended mode on a non-Alpha agent or mutate the runner profile or callbacks per request. The shared runner and embedded coordinator contain mutexes and must not be copied. The capture executor is trusted DI: it must use an immutable registered export, its request-specific resolver/checkers, and the original shared deployment boundary; no real implementation is provided here.

Authentication is required before preview, confirmation, or operation lookup. Existing authentication middleware remains responsible for cookie CSRF checks. The caller must install it for both mounts; bearer authentication retains its existing behavior. A malformed, missing, unavailable, or stale review never invokes a legacy evaluator or direct SDK fallback in configured mode.

## HTTP DTO and review lifecycle

- `POST /api/stacks/{name}/operations/preview` accepts exactly `{"action":"up"}` (or `start`, `restart`, `stop`, `down`). The caller cannot submit flags, selected services, source, actor, target, path or digests. Bodies are closed string-only objects: unknown, duplicate, missing, coerced, multiple JSON values and bodies over 4096 bytes are rejected. Full-stack fixed defaults are the only intended profile; the trusted capture executor must enforce runner fixed arguments, including `up --no-prune --pull never` and `forceRecreate=false`.
- The trusted producer captures source/target under the shared deployment lock. `BeginPreviewV2` durably accepts intent before `Started` and `TerminalPreview` observer callbacks; a successfully stored complete review yields `{requestId, reviewDigest, confirmationToken, review}`. The review is the stored V2 record, not proof of a qualified source. Request IDs are Brine IDs, not Dill IDs. Missing producer/history or failure to durably record the preview returns denial and no token. The accepted request ID, when available, is in `X-Brine-Request-ID` and `Location` for manual inspection.
- To confirm, POST to the original `/api/stacks/{name}/{up,start,restart,stop,down}` with exactly `{"requestId":"br_…","reviewDigest":"sha256:…","confirmationToken":"…"}`. Tokens contain 32 random bytes, live only in process memory for at most 15 minutes, and the current map is bounded to 128 outstanding reviews. A recognized token is consumed before checking mismatch/revalidation and before `BeginApplyV2`; failures, disconnects and mismatches require a **new preview**, never automatic retry. The record links the same durable preview and review digest. Revalidation compares the trusted capture's current context under the shared deployment boundary; it cannot turn mutable workspace paths or JSON claims into source proof.
- `Started` precedes work and `TerminalApply` must persist before a terminal marker can be written. The existing streaming action returns a distinct apply request ID in headers. HTTP 200, disconnect, absent marker or restart does not mean success; process exit, invocation status, outcome/effects, coordination, persistence and delivery remain separate observations. Preview/confirmation are not reservation, transaction, rollback or success prediction.
- Authenticated `GET /api/operations/{id}` uses typed version dispatch and returns the original V1 or V2 record, never a wrapper. V1 remains `UNBOUND`; V2 lookup is observational and cannot create a token or authorize replay. Invalid/missing IDs return 404 and unsafe/corrupt records return a fixed safe failure. Operators must protect this private operational metadata, including resource names.

The browser displays the whole returned bounded review as text, including resources, coverage, diagnostics and context; no names are intentionally truncated or interpreted as HTML. Present resource IDs and planned names have different meanings: planned names are not future IDs or promises. Recreating/removing containers loses writable-layer contents. Named-volume retention is neither backup nor proof of contents or incarnation. Runtime facts may vary; an up no-op may still ensure/start/network. No native qualification or volume-consumer proof is implied.

## Synthetic attended browser fixture (not qualification)

`brine-fixture --synthetic-attended` is an explicitly selected **Linux-only, disposable test fixture**, not a Brine application configuration or production activation. Its producer is fixed synthetic data: it never runs Dill or Pkl, opens an engine/socket, captures source, or proves native/runtime/source qualification. It uses the real Linux operation history store in a private temporary directory under the test account's home. The fixture allows only authenticated synthetic `observability/up` preview and confirmation; normal fixture mode is unchanged. The fixture returns `X-Brine-Fixture: synthetic-attended-no-qualification`.

Run the following only on an isolated non-root Linux GHA worker with the repository's pinned Playwright dependencies and browser installed. This test is not a replacement for native qualification. Choose an unused private loopback port; do not target another fixture or production server:

```sh
go build -o "$RUNNER_TEMP/brine-attended-fixture" ./cmd/brine-fixture
"$RUNNER_TEMP/brine-attended-fixture" --synthetic-attended --port 18179 >"$RUNNER_TEMP/brine-attended-fixture.log" 2>&1 &
fixture_pid=$!
trap 'kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true' EXIT
for i in $(seq 1 100); do
  if curl -fsS http://127.0.0.1:18179/login >/dev/null; then break; fi
  sleep .1
done
BRINE_ATTENDED_FIXTURE_URL=http://127.0.0.1:18179 \
BRINE_FIXTURE_BASE_URL=http://127.0.0.1:18179 \
BRINE_VISUAL_BASE_URL=http://127.0.0.1:18179 BRINE_VISUAL_PASSWORD=unused \
npx playwright test tests/attended-real-fixture.spec.js --workers=1
```

The browser logs in with the fixture-only credential, requests preview, explicitly confirms once, and manually looks up the linked V2 apply record. It asserts no automatic apply or polling. A synthetic `unknown` outcome is deliberately not a success or qualification claim. The fixture must never be connected to a real engine or provided source. Exact-head GHA and independent security review remain required; source factory, application startup wiring, and qualification gates remain separate and off.

## Verification status at this checkpoint

The macOS Go API/web/history race suites and the full local UI suite passed with synthetic fakes and intercepted browser responses. Local UI execution skipped the Linux real-store attended browser test. The Linux real-store API fault/reopen/disconnect tests and this fixture/browser flow were cross-compiled or authored but **not run locally**. The history dependency's independently reported passing CI does not prove this newer controller/fixture head. Run both the real-store Go tests and the browser command above on exact-head Linux GHA; inspect their results rather than treating documentation, HTTP 200 or fake tests as proof.
