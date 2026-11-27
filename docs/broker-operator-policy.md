# Central broker operator policy

Brine bundles Dill v0.12.0 and Pkl 0.31.1. Broker execution is explicitly provisioned, not a fallback for ordinary or retired secret paths. See [runtime qualification](broker-runtime-qualification.md) for scoped evidence and limits.

## Provisioning and startup

Use `--data-dir <canonical-private-root> --init-broker-state` only to create a NEW broker-state directory and permanent broker-protected marker; reuse is refused. Runtime startup never initializes missing used state.

`--broker-policy <absolute-private-file>` loads a private owner-only JSON policy. Authentication is mandatory. Fixed protected state is always checked at `data-dir/broker-state/references.db`, even if the policy flag is removed. Policy removal disables new resolution, not registered-stack protection. Missing DB/directory behind a permanent marker, unsafe state or invalid policy fails startup. Never delete marker/state to recover.

The current private schema is version 2, with immutable deployment intent and append-only outcomes. Older development v1 files are refused; no implicit migration or destructive reset is provided.

## Policy and registration

The policy document follows brokerapp `Policy`, `Registration` and `TargetPolicy` types. Top-level data comprises version, providers, registrations and optional targets. Providers use compiled `secrets.Config`; registrations explicitly allowlist connection IDs for a scope. This is operator metadata, not a browser form schema. Keep credentials in private operator files, never credential/value strings in policy.

Settings may expose connection ID, provider kind, endpoint and scope, but not policy, registry, credential references or Manager. Valid configuration does not establish provider reachability; acquisition status remains unknown until an authorized operation.

Registration creates revision zero and permanently protects the stack. Removing bindings or registrations does not erase protection. Future risky actions use the reviewed controller; ordinary and retired commands remain denied.

## Retirement authority

Empty-generation retirement is distinct from value delivery. Local policy must include `retire`; HTTPS policy must include `retire` and the target must grant `broker-retire` to its separate deploy credential. Prepare/review/apply/monitor grants do not imply retirement authority. Exact authenticated managed predecessor and new empty source/config scope are required. Preparation alone does not deploy; native Plan, ownership, selection, request journal and confirmation still apply. Omitted secret workloads block retirement; volumes, values and historical generations are not deleted. See [setup guide](broker-secrets.md).

## Target pins

Local target configuration may select ProfilePath with explicit Engine, TargetDigest and EndpointDigest instead of Socket/DaemonID; mixed or partial configuration is refused. Local Podman requires explicit configuration. Pins are operator-enrolled Dill metadata, never inferred from web forms, source or runtime success. Dill rechecks profile before non-recovery work; journal/status/fence preserve original-request lookup if profile/runtime files are unavailable. Reload cannot silently retarget or reset the journal.

Namespace-bound local execution requires Brine, Dill and engine in the same qualified user/mount namespace and root view. Brine performs no namespace entry or privileged ownership helper. HTTPS targets retain separate executor namespaces.

HTTPS may set brokerTargetDigest for an independently approved broker namespace commitment. Approved monitor tuple, pinned TLS, signed epoch and distinct deployment credential/grants remain mandatory. The digest does not replace monitor enrollment or change the engine socket. Configuration alone implies no runtime qualification.
