# Brine roadmap: the optional control plane for Dill

Status: **implementation vision**, not shipped feature documentation. Current behavior remains documented in README.md and source. The [ticket index](docs/design/TICKETS.md) contains cross-repository dependencies and acceptance criteria. Companion: [Dill roadmap](https://github.com/umayr/dill/blob/docs/roadmap-architecture/ROADMAP.md), which owns deployment semantics and executable experimental Pkl examples. Those examples are not the current Dill schema.

## 1. Product boundary

Brine makes persistent, self-hosted application stacks manageable over time without requiring a scheduler or Kubernetes cluster. Target audience: homelabs, self-hosters and small internal deployments on explicitly selected hosts. It supplies UI/API, workspaces, Git workflows, approvals, schedules, history, notifications and inventory around a standalone Dill engine.

This is not just Portainer feature parity. The goal is to explain prerequisites, relationships, risks and outcomes using typed configuration and Dill's structured plan. Compose/Portainer are migration inputs and established alternatives; do not claim they lack dry-run, dependency ordering or mature application support. Pkl-generated Compose is a valid alternative when configuration authoring alone solves the problem.

Non-goals for alpha: Kubernetes scheduling/HA, VM/LXC/host provisioning, a new secrets vault, a backup engine, mandatory central registry, general browser/plugin marketplace, or a second container reconciler.

## 2. Current architecture and concrete gaps

At this baseline Brine invokes a local `dill` binary for stack actions, accesses an engine directly for inventory and some mutations, stores local/Git-backed stack files, and stores recipes as copied editor snippets. It has single-admin auth and a LocalAgent, not a finished multi-host system.

Specific implementation cautions:

- `internal/agent/agent.go` exposes `*dill.Runner`. Replace this local-process detail with transport-neutral operations before claiming remote agents.
- `internal/dill/runner.go` serializes by path inside one process. This does not serialize another Brine process or direct CLI. Dill owns canonical target/stack locking.
- Configuring Brine's engine socket must explicitly configure Dill's target too. Engine type equality is not endpoint equality; never let the viewer inspect one machine while deploy mutates another.
- Flat `.pkl` file editing does not cover a complete project: PklProject, lockfiles, nested modules and supporting files need deliberate, traversal-safe handling.
- Process-wide environment inheritance is not per-stack secret isolation.
- Git polling that invokes `up` is not yet an approved-candidate workflow with durable recovery records.
- Direct engine mutations on Dill-managed containers can bypass future policy/locking. Route managed mutations through Dill; distinguish optional unmanaged administration explicitly.

Existing uncommitted development in other worktrees is not incorporated by this documentation PR. Verify the current source before implementing an issue; do not assume a roadmap feature exists.

## 3. One deployment engine, two interfaces

| Responsibility | Dill | Brine |
|---|---|---|
| Pkl schema, semantic IR, reference/DAG validation | Owns | Edits files and displays results |
| Target capabilities, preflight, ownership, plan/apply | Owns | Selects target, requests operations |
| Dependency closure, readiness/jobs, reverse teardown | Owns | Visualizes reasons and outcomes |
| Cross-process target/stack serialization | Owns execution boundary | Queues work, never substitutes a UI mutex |
| Runtime adapters | Owns | Consumes inventory/capability contracts |
| Secret resolution, backup/restore operation semantics | Owns with approved providers | Binds references, schedules, displays evidence |
| Git workspace, source revision and editor | Can consume ordinary files | Owns managed workspace workflow |
| Approval and scheduling | Enforces supplied candidate/policy | Owns operator interaction and automation |
| Durable execution outcome | Emits/records authoritative result | Persists/indexes redacted history |
| Web auth, notifications, fleet inventory | Not required | Owns |

Pkl owns package dependency resolution/checksums. Operator policy owns authorization. Recipes cannot install providers or authorize privileged operations. Brine must never require its database for ordinary standalone Dill application configuration to remain usable.

### Local protocol first; transport-neutral contract

Keep subprocess execution initially. Negotiate protocol and capabilities rather than parse human output or infer support solely from version strings. Define these operations with Dill:

```text
Capabilities(target) -> versions, identity, supported operations/checks
Validate(project or artifact) -> typed/semantic findings
Preflight(intent, target) -> PASS/FAIL/UNKNOWN/DEFERRED evidence
Prepare(intent, target) -> explicit artifact preparation result
Plan(candidate, target) -> changes, affected graph, risks, preconditions
Apply(candidate, approval) -> operation ID + progress + final result
OperationStatus(id), Cancel(id), Logs(...)
```

These are conceptual contracts, not existing API routes or method signatures. Versioned request/result/event fixtures must be shared between repositories. JSON stdout is machine-readable, stderr diagnostic. Preserve Dill's plan-vs-diff exit semantics; output format must not turn failure into success. An artifact render is not a saved approved plan.

## 4. Typed configuration without a second schema implementation

Maintain four separate domains:

1. **Application intent** in Pkl: services/jobs, dependencies, storage and secrets required.
2. **Deployment bindings** in Pkl: explicit target, host resources, ports and provider references.
3. **Trusted Brine/server settings**: transports, auth, provider registrations, source defaults and effective policy; separate schema from application intent.
4. **Operational records**: job IDs, selected revisions, approvals, progress, outcomes and audits; not application source.

Example of the intended binding surface (illustrative fragment; full executable Pkl is in Dill's design fixtures):

```pkl
bindings {
  secrets {
    new {
      name = "database-password"
      provider = "host-files"
      key = "catalog/password"
    }
  }
}
```

Public declarations and bindings use typed named listings (`new { name = "…" }`), not empty keyed objects. Environment variables and labels remain naturally keyed mappings. The editor must preserve stable names, not use array positions as identities. Reordering declarations is not a request to reorder startup or recreate services: Dill validates duplicate names and references, normalizes indexes, and computes the DAG. Brine displays those findings instead of silently deduplicating entries.

The UI stores a reference, not the password. Typed forms should consume a versioned recipe input contract; do not infer arbitrary Pkl expressions or rewrite user code with regex. Generate a separate binding/amendment module where practical, leave the editor as the fallback, and validate using Dill.

### Workspace modes

- **Git-managed:** record exact source revision and explicit local changes. Edits become intentional commits/PRs or clearly marked local modifications. Never overwrite dirty files or push without intent.
- **Brine-managed:** workspace files are authoritative and exportable. Metadata/DB indexes cannot silently override them.

Support PklProject, PklProject.deps.json, nested Pkl and approved ancillary files while rejecting traversal/symlink escapes. Dependency resolution is explicit and reviewable. Applying a candidate must not implicitly update packages, source catalogs or Git HEAD.

Document precedence (explicit flags > supported environment overrides > selected configuration > defaults) for server settings. Do not inject the entire server environment into arbitrary stack evaluation. Server bootstrap/auth must not depend on evaluating stack-supplied Pkl. Test actual Pkl resource/module restrictions; type safety is not a sandbox.

## 5. Operation workflow and history

```text
selected source revision + deployment bindings
  -> offline validation
  -> read-only target preflight
  -> explicitly authorized preparation
  -> candidate plan and risk review
  -> approval bound to digest/target/policy
  -> Dill apply-time revalidation and execution
  -> post-start verification
  -> persisted outcome and recovery options
```

Brine should answer: which revision is deployed; why a service will restart; which dependencies are affected; which checks are unverified; which data is at risk; what happened; and what can safely be recovered.

Show UNKNOWN as unknown, not success. A failed storage mount check should prevent the user from mistaking an empty directory for their NAS. Namespace-owner replacement should display dependent recreation. Job completion is different from running state. Brine must not compute a parallel DAG or decide a job succeeded from a transient container status.

Persist states such as queued/running/succeeded/failed/cancelled/unknown. A browser/SSE disconnect is not an automatic request to kill deployment. Restarting Brine must reconcile an existing operation rather than blindly reissue it. Cancellation is explicit and its partial effects visible. Ambiguous job completion cannot be retried automatically unless the execution contract safely permits it.

Approval is bound to candidate identity, target, policy and revision. A changed Git revision or image/precondition invalidates it. Never render an old plan beside a button that deploys a new revision. Cleanup is not rollback, and restoring old containers does not undo database migrations.

## 6. Secrets, authorization and trust

Separate application secrets, registry/Git credentials and Brine login/session/API material. Prefer target-side resolution through Dill; keep plaintext out of Git, rendered artifacts, plans, SSE, stored logs and diagnostic bundles. Environment injection remains visible to engine inspection in many runtimes; describe this limitation honestly.

Per-deployment secret/environment bindings must not become global Brine environment variables. Operator-configured providers control where values come from. Existing host-mounted secret files remain viable; do not force users to upload secrets into Brine. Rotation is explicit and follows Dill's recreate/version policy.

Keep single-admin alpha scope honest. Preserve CSRF/auth protection and engine-socket privilege warnings. API tokens need explicit actor attribution and eventual scopes/rotation. External identity/RBAC is a follow-up contract, enforced server-side for target/stack and binding access—not only UI visibility. Audit records identify actor/action/target/revision without values. Target/agent enrollment, server identity and revocation are prerequisites for remote trust; never expose unauthenticated engine TCP.

## 7. Open recipes and control-plane integrations

A catalog is bounded versioned JSON, potentially authored using Pkl, pointing to canonical immutable Pkl packages. Anyone may host sources; users choose trust. Search must not execute catalog code. Qualified names avoid ambiguity; show actual package host, source and selected version. Direct URI and local development workflows remain first-class.

Distinguish snippets (copied text), components (typed imports) and blueprints (complete starting stacks). Generate normal Pkl projects and explicit dependencies; use Pkl's resolution/lockfile, not a Brine resolver. A generated deployment must still work without Brine or the catalog. Package checksums do not by themselves authenticate a publisher. Upgrades are reviewed dependency changes plus a Dill plan.

Start integration work with authenticated Git webhooks, notifications and scheduling. Require idempotency/replay protection, bounded queues, timeouts and secret-safe destination handling. A duplicate webhook must not double-deploy. Notification failure must not change a deployment result or trigger a deployment retry. Consider outbound SSRF and credential disclosure explicitly.

Backup schedules invoke Dill/provider operations; Brine does not reinvent database consistency or retention semantics. Approved external processes need versioned capability protocols and least-privilege environment handling; they are not automatically sandboxed. No recipe-installed executables or arbitrary browser plugins in alpha. Existing Watchtower/Autoheal behavior remains explicit during coexistence rather than silently replaced.

## 8. Multi-host and homelab migration

One control plane may eventually address multiple explicit targets. Decide SSH executor versus authenticated agent transport through a bounded design spike. In either case, host checks/secret resolution/locking must occur at the execution target. An unavailable host is unknown/unreachable, not an empty healthy inventory.

Do not add automatic scheduling, failover or distributed storage. Apple container/Incus application capability experiments belong in Dill; Brine consumes the resulting capabilities instead of implementing another adapter. LXC/VM provisioning stays external.

Migration acceptance cases are derived from real requirements but use synthetic fixtures only:

| Fixture | Required evidence |
|---|---|
| Dashboard with credentials | Isolated bindings, valid plan, logs and safe trial deployment |
| VPN + two namespace consumers | Dependency closure and dependent recreation shown correctly |
| Database initializer | Completed/failed/unknown job results; no blind retry |
| Worker with custom entrypoint | Process semantics and host capability checks |
| Storage-backed application | Exact existing storage identity and mount failure handling |
| Multi-host inventory | Viewer and lifecycle share the same authenticated target |

Keep `hl` snapshots, no-prune intent, verification and recovery workflow during homelab adoption. Never offer blind adoption by matching Compose labels/names. Preserve actual volume identities, published endpoints, Watchtower/Autoheal labels and secret bindings. Trial instances use distinct ports/names and isolated writable data. Do not start two databases against production storage or revive inactive legacy stacks.

Changing the control plane also affects Alloy's Compose-label attribution and Portainer-specific dashboard widgets. Caddy, DNS, host configuration sync and backups remain explicit integrations. No production migration is authorized by these documentation/issues.

## 9. Phases and execution order

The [ticket index](docs/design/TICKETS.md) is authoritative for prerequisites. Phase labels are outcomes, not a parallelization instruction.

| Phase | Brine outcome | Gate |
|---|---|---|
| 0/1 | Target-aligned negotiated protocol | Views and mutations hit the same target; managed writes go through Dill |
| 1 | Authoritative workspaces, isolated bindings, honest operations | Full Pkl project usable; no secret leakage; stale approvals fail |
| 2 | Secure explicit multi-host, diagnostics and migration UX | Failed host/check is visible; isolated pilot verified |
| 3 | Recipe UX, schedules/events, recovery-aware history | Standalone portability; duplicate events cannot double-deploy |
| Later | Broader auth/integrations and new Dill capabilities | Narrow contracts proved before generic extension APIs |

Do not block a local safety fix behind an entire remote-agent feature. Conversely, do not ship remote writes before endpoint identity, authentication and target locking are sound. Break larger issues into independently verifiable sub-PRs while retaining acceptance criteria.

## 10. Verification requirements

For every feature add tests at the boundary it changes: runner contracts, authorization, file security, operation lifecycle, browser behavior and integration with a compatible Dill binary. Use synthetic data and secret sentinels. Include negative/cancellation/restart/outage cases, not only successful screenshots. Report exact commands and what was not tested.

Run relevant `make test`, `make vet`, build and browser checks from the worktree. UI tests and contracts must not depend on production homelab endpoints. Dill owns executable Pkl design fixtures; run those when changing shared model assumptions. Evaluate compatibility together before merging either side of a protocol change. Never claim a capability based solely on documentation or type-checking.
