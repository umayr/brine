# Central broker integration

Brine's image bundles Dill v0.12.0 and Pkl 0.31.1. Broker execution requires
explicit operator provisioning and the matching engine/executor filesystem view.
Code/contract, synthetic browser checks and actual disposable-target evidence are
reported separately in [runtime qualification](broker-runtime-qualification.md).

Main Plan now reviews captured source files before authenticated transfer, then
shows native Dill differences and grants before a separate one-shot confirmation.
Unresolved requests expose status-only lookup in shared History. The web profile
includes bounded effective non-secret configuration variables in its immutable
captured input document. Git checkout subfolders use anchored workspace-descriptor capture. Remote-Git automation is independent: only a disabled, resolved policy permits
manual review of its current local checkout. Enabled or pending/unknown policies
remain protected; this flow does not fetch a branch or turn on automatic rollout. The authenticated source/Plan/confirmation/unknown/status workflow has synthetic
coverage. The production browser Settings/Variables/Plan/confirmation/History flow
also passed real file acquisition and Docker consumption. Explicit empty-generation
retirement and update/restart/reconciliation passed on the enumerated Docker,
Podman and rootless runtime cases. A subsequent actual Infisical browser-to-container
case passed using a separately hosted instance and a fresh sandbox. These scoped
results do not establish an exhaustive provider/profile matrix or automatically
qualify every packaged deployment arrangement.

- `internal/secrets`: operator-configured Vault KV v2, Infisical and allowlisted
  local files. Credentials stay in operator file references, never browser input.
- `internal/secretbroker`: explicit private reference-only state and frozen
  in-memory preparations. Missing/unreadable state is not an unprotected stack.
- `internal/brokerwire`: mirrored `dill.broker-secrets/1` contract in Dill v0.12.0.
  Only explicit Encode creates a transport payload; clear it after use. General
  marshaling/formatting refuses/redacts values. Requirements are metadata only.
- `internal/agentclient.BrokerClient`: separate monitor proof and deploy
  transports, exact enrolled TLS pin and signed binding, no redirects/replay,
  per-operation local grant ceiling and current-policy gate. Scope, journal,
  source, target, physical endpoint and returned reference are checked. Lost apply
  delivery is unknown; query the same request ID, never automatically resubmit.
  Existing fence/status recovery needs its own local and remote grants.
- `internal/dill.DecodeBrokerReview`: validates native preflight and retains
  native service/field diffs; never fabricates a successful plan from empty data.

## Final-dispatch ownership

The controller must hold the existing stack/source deployment lock first, then
use Manager.Dispatch for one-shot bytes and Manager.WithCurrent for reference-only
apply. Those hold the policy read fence and an immediate SQLite revision pin
through the transport callback. All binding writers (even another Store handle)
are excluded. All policy writers must use ChangePolicy with an immutable swap.
Prepare/Inspect authorization also uses the policy fence; Close waits for final
dispatch. Callbacks must not reenter this binding Store, mutate policy or close
the Manager. Keep transport deadlines bounded. Manager shutdown cancels in-flight acquisition,
waits for final dispatch pins, and clears retained preparations. Acquired Value
instances are explicitly closed after copying into the frozen preparation;
Close invalidates all copies of that Value but not caller-owned byte copies.
Clearing is best-effort lifetime reduction, not a memory erasure guarantee.

A post-callback storage/fence error does not prove no target effects. Preserve
the separately observed status and durable request record. Application history
must durably record intent BEFORE submitting apply and preserve exact target,
journal, reference, source and request ID; these libraries do not do that for it.
Policy/session/source/target authorization is still the controller's duty.

## Connected application workflow

Application startup now opens the broker service and composes protection with
legacy actions/validation and direct container lifecycle classification. Settings provider metadata and Variables reference-only binding handlers/templates
are wired and synthetic-browser-tested. Shared History now renders reference revisions and immutable deployment metadata.
Native unified Plan/confirmation/status handlers are wired and synthetic-browser-tested. Local CLI now uses dill.LocalBroker with a strict per-action grant ceiling and
current-policy gate, through Dill's administrator-local journal/stager/broker
adapter. It supports durable request ID lookup and fences, not ordinary/retired
operation fallback. A paired native test verifies journal/fence/status and source
stage/manifest without engine/Pkl/provider execution. Provision target state
explicitly, never from a browser handler.
The private application store now has immutable deployment intent and append-only
outcome APIs. They must be used before/after final dispatch (not reentered within
the binding pin). Controller composition and actual local/HTTPS adapters now exist, including
one-shot submit and status-only reconciliation. Main Plan/confirmation includes
source/session guards and request-scoped approved-target lifetime handling.
Effective non-secret variables are captured under source consent and rechecked
under the variable lease at final dispatch. Git subfolder capture opens every component through the validated store workspace
descriptors; sibling repository files are not captured or consented. Disabled and resolved remote-Git automation policies may use their current local
checkout through this manual flow. Enabled automation or any pending/unknown request
continues to block capture; disabling automation never clears an unresolved request.
Explicit local Docker/Podman/rootless policy pins and independent HTTPS Docker
broker commitments are wired; see [operator policy](broker-operator-policy.md).
These are configured identities, not runtime health evidence.

## Explicit retirement

Remove secret declarations/mounts from retained services and remove their references
in Variables. Plan discovers the exact empty requirement set and prepares a distinct
metadata-only retirement, without acquiring provider values. Confirmation explicitly
identifies retirement and retains the native Dill diff. Local `retire` or HTTPS
`broker-retire` authority is required in addition to review/apply grants.

Predecessors come only from durable successful/partial application intents for the
same runtime group, never browser input. Dill independently verifies active MAC-bound
generations, the current managed target-journal predecessor and exhaustive live
workload coverage. Omitted secret-using workloads block retirement: retain them in
source without mounts or resolve them through a separately reviewed lifecycle
operation. Empty bindings alone never remove protection or enable ordinary deploy.

Unresolved requests block new preparation. Retirement has the same source/session,
policy/binding/target fences, one-shot confirmation, intent-before-apply and
same-request status-only reconciliation as delivery. History permanently records
retirement metadata and outcomes. Historical generations, provider values, volumes
and protection records are never deleted. Predecessor/document/history capacity
limits fail closed; this flow performs no automatic cleanup.

Ordinary remote review/stage/confirm/removal and Git-automation admission are
serialized with permanent broker registrations. Registration cannot race a
previously unprotected dispatch; a registered stack cannot use that ordinary path,
even with empty bindings or removed provider policy. Original-request status,
fencing/recovery and disabling automation remain available. A failed final state
fence returns an unavailable/unknown result, not an already-buffered success.
Guard callbacks must not reenter broker state or policy/lifecycle APIs. This
conservative SQLite admission pin can delay binding writes while an ordinary
submission is in progress.

Pkl remains executable operator-trusted source; bounded machine diagnostics are
NOT sandboxing. Broker execution requires pre-existing never-pull images and a
retained qualified target. Legacy rootful Docker requires positive Linux/security
metadata and refuses rootless/remapping. Explicit namespace-bound Docker and local
Podman profiles additionally require mapped owners and the same engine/executor
user/mount namespaces and root view. Custom child user-namespace maps are refused;
no automatic namespace entry or privileged helper exists. HTTPS remains Docker-only.
Actual file/Vault acquisition, local/HTTPS Docker delivery and explicit local
Podman/rootless profiles have the scoped passing evidence listed in
[runtime qualification](broker-runtime-qualification.md). Rootless executors used
operator-owned private filesystem roots; ordinary unmapped host-root ownership
still fails closed. Synthetic TLS/CLI and race checks are not substitutes for the
listed runtime evidence. Infisical creation is separately verified; full Infisical
recovery/retirement and exhaustive cross-product coverage are not claimed.
