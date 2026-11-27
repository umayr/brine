# Central broker operator policy (development)

The image bundles Dill v0.12.0 with Pkl 0.31.1. Broker execution is an explicit
operator-provisioned path, not a fallback for ordinary or retired secret paths.
See [runtime qualification](broker-runtime-qualification.md) for the exact tested
profiles, released-asset CI and remaining coverage limits.

## Explicit provisioning and startup

The tested CLI boundary is `--data-dir <canonical-private-root> --init-broker-state`.
It creates a NEW broker-state directory and a permanent broker-protected marker;
it refuses reuse. Runtime startup never initializes/recreates missing used state.

`--broker-policy <absolute-private-file>` loads a private owner-only JSON policy.
Authentication is mandatory. Fixed protected state is always probed under
data-dir/broker-state/references.db, even if the policy flag is later removed.
Policy removal disables new resolution, not registered-stack protection. Missing
DB, missing directory behind a permanent marker, unsafe state or invalid policy
fails startup. Never delete the marker/state as a recovery procedure.

Current private schema version2 adds immutable deployment intent and append-only
outcome tables. Older development v1 files are refused; there is no implicit
migration, destructive reset or promise of released-schema compatibility.

## Policy shape

Top-level keys are version (1), providers, registrations and optional targets. providers uses the
compiled secrets.Config structure (Version:1, Connections map). Each connection
contains exactly one Vault, Infisical or Files configuration with operator file
references, never credential/value strings. Registrations contains Scope
(RegistrationID: canonical32hex, StackName, TargetRegistration) and Connections
(an explicit connection-ID allowlist). This is operator metadata, not a browser
form schema. Registry construction validates HTTPS endpoints/scopes/file aliases
without claiming credentials or provider reachability were tested.

Settings may display Service.Connections metadata (ID, human provider kind,
endpoint, scope). It must not serialize the policy, registry, credential file
references or Manager. Connection acquisition status is unknown until an actual
authorized operation, never implicitly successful from valid configuration.

Registration is explicit in this operator policy, creates revision zero and
permanently protects that stack. Removing bindings or registrations from current
policy does not erase durable protection. All future risky actions must use the
new reviewed controller; ordinary/retired commands remain denied.

## Retirement authority (development)

Target preparation for empty-generation retirement is separate from value delivery.
Local target policy must explicitly include `retire`; HTTPS Brine policy must
include `retire` and the target must grant `broker-retire` to its separate deploy
credential. Existing prepare/review/apply/monitor grants do not inherit this power.
The exact authenticated managed predecessor and new empty source/config scope
are required. Preparation alone does not deploy; native Plan, ownership, selection,
request journal and confirmation still apply. Brine controller/Plan/History
retirement wiring is implemented and synthetic-tested. No prune: omitted secret workloads block retirement;
no volumes, provider values or historical generations are deleted.

## Execution profile pins (development)

Local target configuration may select ProfilePath with explicit Engine,
TargetDigest and EndpointDigest instead of Socket/DaemonID. Mixed or partial
configuration is refused. These pins are operator-enrolled Dill metadata, never
learned from a web form, source file or runtime success claim. Local Podman
requires this explicit configuration. The actual Dill child receives profile and
pins only, and independently rechecks the profile before non-recovery work.
Journal/status/fence preserve original-request lookup when profile/runtime files
are unavailable. No profile reload can silently retarget or reset the journal.

A namespace-bound local executor inherits the Brine process namespace: the
operator must arrange the same qualified user/mount namespace and root view for
Brine, Dill and the local engine. Brine never invokes namespace entry or a
privileged ownership helper. Separate HTTPS targets keep their own executor
namespace. No runtime qualification is implied by configuration.

HTTPS target policy may set brokerTargetDigest for an independently approved
broker namespace commitment. The complete approved monitor tuple, pinned TLS,
signed epoch and distinct deployment credential/grants remain mandatory; this
field does not replace monitor enrollment or change the engine socket. Missing
explicit pin retains the original rootful Docker broker identity, not server
inference. This HTTPS agent remains Docker-only; remote Podman is not supported.

Native broker review decoding and controller dispatch use the exact reported
engine bound to the configured target, while ordinary preflight remains
Docker-only. Profile names/paths and credentials are never surfaced in stack UI.
Dill now explicitly selects the qualified default engine user namespace for
broker Podman creation and refuses custom/private/keep-id maps during raw census
and repeated inventory. Scoped file/Vault, local/HTTPS Docker and local Podman
runtime results are recorded in [runtime qualification](broker-runtime-qualification.md),
including rootless Docker/Podman. This is not an exhaustive matrix or released
support. Dill broker-profile-info still exports only value-free offline identity
pins; it never claims engine/provider health or performs namespace entry.

Rootless ancestor ownership is not relaxed: a normal host-root filesystem can
appear unmapped inside the engine user namespace and must fail closed. Qualified
fixtures use an explicitly operator-started, namespace-UID0-owned private root/state
and matching engine/executor user/mount/root views. Namespace bootstrap precedes
switching root views; no request handler enters namespaces or invokes a privileged
secret-ownership helper. Never treat the overflow UID as initial-host root.

## Durable application records

BeginDeployment commits immutable identity/source/target/reference, request,
stage, journal, selected mode and review digest BEFORE target dispatch.
AppendDeploymentEvent adds evidence without overwriting prior entries. Unknown
may be reconciled later; terminal evidence cannot be silently rewritten.
Deployments returns scope-checked history only. A pending record after restart is
unresolved, not proof that no delivery occurred. No values, source files,
credential strings or restoration payloads belong in these tables.

The controller must not call these Store mutation methods inside WithCurrent's
binding transaction. Commit intent first, execute the pinned callback, then
record the separately observed outcome after the pin is released.
