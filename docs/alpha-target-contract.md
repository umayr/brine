# Explicit Alpha target boundary

## Selection and compatibility

Brine defaults to `--profile legacy` (also selected when the profile is omitted).
This deliberately retains existing ambient/default host resolution at startup,
including `DOCKER_HOST`, `PODMAN_SOCKET` and engine defaults. It is compatibility
behavior, **not** the explicit Alpha execution boundary. Prefer an explicit socket
in legacy mode too. Startup resolves one effective host and gives it to both
SDK observations and the Dill runner.

`--profile alpha --engine docker --engine-socket /absolute/disposable.sock`
(or `--engine podman`, or `unix:///absolute/disposable.sock`) selects the explicit
boundary. Alpha rejects an omitted, relative, noncanonical or remote socket and
unknown engine/profile. Alpha also requires authentication, even on loopback;
`--profile alpha --no-auth` is rejected before any startup side effects. Legacy
loopback `--no-auth` compatibility remains outside this profile. It does not
derive target identity from a recipe,
container label, ambient environment or a Dill capability document. Configuration
validation does not open the socket or certify that it exists or is ready.

The header and stack detail display the actual selected engine/socket and profile.
They label this **configured only**, not negotiated or readiness-checked. An
engine kind is not a unique host identity. Secret target names, when configured
using the separate strict-secret options, are operator-selected policy names,
not negotiated host identities either.

## Released Dill evidence and concrete blocker

Inspected read-only public source at **Dill v0.7.0**, annotated tag
`e6aacf276a460b7b338696b5dd813ff790b0089a`, commit
`0554f6d73cb924b06a47f3a495bcad39a9dcb08c`:

- [`internal/protocol/request.go`](https://github.com/umayr/dill/blob/v0.7.0/internal/protocol/request.go):
  `dill.bootstrap/1`, `dill.dev/1`, only `protocol.check`; the request admits no
  target, source, engine, socket or application payload.
- [`internal/protocol/codec.go`](https://github.com/umayr/dill/blob/v0.7.0/internal/protocol/codec.go):
  discovery advertises empty IR, target and provider capabilities. A successful
  check has no engine effects, not evidence of target identity or readiness.
- [`docs/design/protocol-contract-19-proposal.md`](https://github.com/umayr/dill/blob/v0.7.0/docs/design/protocol-contract-19-proposal.md):
  metadata-only foundation; no apply, target identity/security policy, ownership,
  provider or durable execution contract. Transport requires connected Unix
  stream sockets on Linux; pipes/files and all Darwin descriptors are rejected.
- [`docs/capabilities.md`](https://github.com/umayr/dill/blob/v0.7.0/docs/capabilities.md):
  `caps` describes adapter representation limits without contacting a host;
  it is explicitly not target preflight or the operation protocol.

The separate `dill.alpha-secrets/1` discovery is not a general target/ownership
execution contract either. Brine does not invent capability names, send target
properties to this closed protocol, wrap legacy output as protocol records, or
use a successful metadata check as mutation permission. A future supported Dill
contract must define operation/target identity binding, ownership and no-prune
semantics, fail-closed mismatch/version behavior and appropriate transport before
Alpha writes can be safely enabled. **Full issue #65 negotiation is blocked, not
implemented or ready to close.**

## Enforced Alpha operation matrix

| Surface | Behavior |
| --- | --- |
| SDK inventory, inspect and logs | Use the selected explicit socket; errors remain errors on existing surfaces |
| Ordinary saved/draft validation | Trusted store stack alias, engine and explicit socket passed to Dill; unsupported flags/commands fail, with no unqualified retry |
| Plan, deploy, start, restart, teardown | Rejected before source interpretation, secret fallback or Dill execution |
| GitOps changed-pull deployment | Same runner rejection; Git pull/status bookkeeping may still occur, but no engine mutation |
| Direct SDK container start/stop/restart | Rejected, including unmanaged IDs; no label/recipe-based exemption |
| Direct SDK image pull/remove (including force) | Rejected; no global prune or reconciler |
| Secret preview and confirm/apply, active-binding apply | Rejected before activation or subprocess mutation; strict-secret options cannot bypass the profile |
| Pinned stack stop/down (including delete-with-down) | Retained as label-only cleanup via Dill; trusted alias, engine, socket and a guaranteed-missing recipe in a private runtime HOME |
| Offline formatter and source editing | Retained; formatting is not engine execution |

Cleanup does not require a readable recipe, available secret sources, working
Pkl evaluation, or target negotiation. It still requires a compatible Dill that
honors explicit endpoint/stack flags and its existing label-cleanup contract.
Failure, cancellation or timeout is never cleanup success. The direct container
Stop API is deliberately **not** this cleanup escape hatch.

UI disabled states and explanatory text are presentation only. Server-side runner
and SDK boundaries reject manually crafted API/UI requests as well. Legacy mode
continues its existing guarded direct SDK operations, but all Brine ordinary
saved/stdin validation now receives the same trusted alias and endpoint flags.
Older Dill CLIs lacking these flags are deliberately incompatible for validation;
Brine never retries against their built-in default.

## Regression evidence and limits

Permanent synthetic two-endpoint tests deliberately supply a wrong ambient and
fake-CLI default. They exercise Docker/Podman selections, API lifecycle, UI/API
plan and saved/stdin validation, direct container/image routes, SDK inventory and
logs, displayed identity, and actual changed-head GitOps Up. They assert positive
selected operations, **zero wrong-endpoint requests/writes**, and Alpha bypass
rejection. Strict background old-Dill rejection now proves changed HEAD, recorded
new commit and actual discovery before asserting no Up. These are contract/routing
tests, not real Docker/Podman conformance or browser execution.

The cleanup regression's original one-second subprocess budget was reproduced
failing in a full concurrent race run: the stop route took 1.002s and reported
`context deadline exceeded`, with no completed fake command. The source/endpoint
safety fixture now has a bounded ten-second scheduling budget, preserving every
exact command/source/no-mutation assertion. Separate expired-deadline tests
require cleanup errors and prove no subprocess execution. Production timeouts
were not increased. This identifies timeout exhaustion, not a guarantee about
OS scheduler, process-start or filesystem latency on other machines.

No real engine, deployed service, operator secret input, or production mutation
is needed for these tests. Released/candidate integration suites remain opt-in;
no real parity, target attestation or deployment readiness is claimed.
