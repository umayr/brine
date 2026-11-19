# Brine and Dill: current state and roadmap

This is a source-based snapshot of Brine `4f9ca71b` and the inspected Dill checkout `5e493fda`, not a release or production qualification. “Shipped” means present in these trees, not validated in every deployment. Brine's [longer roadmap](../ROADMAP.md) is a vision; older Alpha documents are historical where their instructions conflict with current disablement.

## Shipped / present

- **Local control plane:** Brine provides stack editing and lifecycle through a local Dill subprocess, engine inventory and some direct container/image mutations, Git-backed stacks, non-secret variables, and single-admin web/API access. The image defaults to Dill v0.8.0, which is distinct from the inspected Dill source checkout. Variables are not secret storage. See [README](../README.md), [runner](../internal/dill/runner.go), and [server startup](../cmd/brine/main.go).
- **Standalone Dill:** ordinary stack operations, operation/coordination interfaces and ordinary no-prune paths remain available under their guards; current Dill also has experimental local `dill-serve` health/status and opt-in snapshot staging, separate from the default build. Staging is not deployment or secret delivery. See Dill's [disabled-entrypoints status](https://github.com/umayr/dill/blob/5e493fdaf1b32363b4b3d620b1422be77725d280/docs/disabled-alpha-secrets.md) and [service contract](https://github.com/umayr/dill/blob/5e493fdaf1b32363b4b3d620b1422be77725d280/docs/dill-serve.md).
- **Opt-in integration code:** Brine has authenticated pinned target enrollment and remote read UI, plus separately enabled audited environment reveal; Dill has an experimental Docker-only host agent for bounded inventory, inspect and logs, and opt-in signed reveal. Brine also has an opt-in local snapshot-staging path to `dill-serve`. These are narrow interfaces, not remote deployment. See [enrollment](target-registry.md), [reveal](remote-reveal.md), [configuration](../internal/config/config.go), and Dill's [host agent](https://github.com/umayr/dill/blob/5e493fdaf1b32363b4b3d620b1422be77725d280/docs/host-agent.md).

## Disabled, not merely unfinished

- Brine rejects Alpha profile/options, qualified operations, operation-history options and strict-secret workflows. Dormant codecs, tests, synthetic fixtures and historical instructions do not activate them. Existing secret state and downgrade/unknown-history guards remain: **do not delete or bypass them**. See [startup](../cmd/brine/main.go), [secret gate](../cmd/brine/secret_gate.go), and [disabled-feature tests](../internal/dill/disabled_features_test.go).
- At the inspected Dill revision, Alpha disposable-volume and strict file-secret entrypoints are disabled; secret declarations fail closed and secret-history protections remain. Historical paired Alpha tests and releases are not evidence that these functions work at this revision. Ordinary operations must not bypass those protections.

## Present but unqualified

- **Remote reads and reveal:** real SSH transport and provisioning are not qualified by the synthetic evidence; independently verify SSH, agent and engine identities. Dill's agent does not authenticate socket callers; same-UID access and engine sockets are privileged trust boundaries, not a sandbox. Podman is not supported by that agent CLI. Reveal requires separately provisioned keys and audit state; delivery, revocation and erasure are not guaranteed. See [target registry](target-registry.md) and [remote reveal](remote-reveal.md).
- **Cross-application Alpha:** historical paired Docker/rootless Podman results cover particular prior commits and fixtures, not current disabled functionality, complete browser/server deployment, restart recovery, or arbitrary environments. See [historical evidence](alpha-secrets-status.md) and [operation evidence](alpha-operation-consumer.md). No new real-engine, real-SSH or browser qualification is asserted here.
- **Project transfer:** staging moves potentially sensitive project files, not an approved execution. Dill's receiver has stricter path rules than Brine's builder; compatibility and explicit whole-file-list consent matter. Trusted local filesystem, same-user and retention assumptions apply; hashes alone do not authenticate a sender. See Dill's [receiver limitations](https://github.com/umayr/dill/blob/5e493fdaf1b32363b4b3d620b1422be77725d280/docs/project-snapshot-receiver.md) and Brine's [sender](../internal/projectstaging/sender.go).

## Planned / dependencies

1. Establish transport-neutral negotiated contracts and matching endpoint identity before remote writes; Dill remains the execution/coordination authority, while Brine owns operator interaction and workspace. A local mutex or successful inventory is not cross-process locking or target proof.
2. Build trustworthy project capture and review, explicit transfer/retention, and compatible Dill receiver validation before any source execution. Approval must bind source, target and policy; staging alone grants no execution authority.
3. Re-enable any qualified operations, history or secrets only after explicit design, compatible paired versions, retained-state safety, real target/runtime tests and end-to-end browser/restart/recovery qualification. Do not infer re-enablement from old documentation or capability pins.
4. Later work includes approved Git automation, recovery-aware history, multi-host mutations, richer recipes, scheduling and integrations. Dependencies and acceptance criteria are in the [ticket index](design/TICKETS.md); phase labels are not claims of implementation.

## Evidence limits

This document summarizes checked-in source and documentation at the revisions above; it does not establish deployed versions, release-artifact equivalence, production safety, or live service behavior. Historical evidence is deliberately scoped to its recorded revisions and test environments. Consult source and rerun relevant isolated integration tests before relying on a capability; neither a passing unit test nor a protocol type alone proves qualification.
