# Qualified Alpha operation consumer — candidate b3

This implements Brine #66's bounded **attended CLI consumer**, not #67 durable
review/source approval, #68 deployment security qualification or #69 native
application/browser acceptance. **Actual native Brine + Dill b3 + browser canary
has NOT RUN; qualification remains pending.** Synthetic tests are not engine or
host evidence. No production use, host provisioning or resource creation is
performed by this integration.

## Exact candidate and trust boundary

The reviewed Dill candidate is main
`b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2` (tree
`cc545129c5a178cd07fa97b9e7827f74fe624866`). The Linux amd64 binary SHA256 is
`3acea65d4121402194787bb0bdccc20a79c725e1ee5bb2337cc5b33b16e12d51`;
public handoff tar SHA256 is
`75a108b6537457b36c4b6aa75f25895683e60d45e104c6fe8085016584a34bcd`.
Use externally reviewed provenance/checksums before extracting/executing artifacts;
an archive's own manifest cannot authenticate itself. Brine does not download,
install or select a new candidate automatically. Choose its executable explicitly
with `--dill /absolute/operator-installed/b3/dill`. The bundled/released **Dill
v0.7.0 remains unchanged and incompatible with this operation contract**. Version
text or metadata `protocol.check` does not negotiate execution.

Pinned complete producer docs:
- [operation-v1](https://github.com/umayr/dill/blob/b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2/docs/operation-v1.md)
- [local coordination](https://github.com/umayr/dill/blob/b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2/docs/local-mutation-coordination-design.md)
- [qualification boundary](https://github.com/umayr/dill/blob/b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2/docs/alpha-coordination-qualification.md)
- [candidate handoff](https://github.com/umayr/dill/blob/b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2/docs/alpha-candidate-handoff.md)

## Explicit configuration, never automatic provisioning

All three coordination flags are required together and must be nonempty. Only
**authenticated Alpha, native Linux Docker** is supported. A trusted external
operator must first establish a disposable host with native Brine and CLI under
the same dedicated operator account/namespace, fixed local Docker socket,
independently known expected daemon ID, and shared canonical preexisting
operator-owned **0700** state outside Brine's stack-editable data directory.
Startup checks state ownership/modes, symlink-free safe ancestors and Linux access
and default ACL absence without creating or repairing anything. Dill revalidates
its own state each invocation; startup checks are not host attestation/readiness.
The operator must independently qualify the local filesystem flock/sync/link
semantics. Do not derive the expected ID by asking the newly selected socket who
it is; Brine never does so. Do not reuse CI daemon identity or copy its private
state/key/journal.

Illustrative launch shape only, **not permission to target an existing engine**:

```sh
/path/to/brine --profile alpha --bind 127.0.0.1 \
  --engine docker --engine-socket /absolute/disposable/docker.sock \
  --data-dir /absolute/private/brine-data \
  --dill /absolute/operator-installed/b3/dill \
  --coordination-layout native-local-docker-v1 \
  --coordination-daemon-id INDEPENDENTLY_PROVISIONED_EXPECTED_ID \
  --coordination-state-dir /absolute/private/shared-coordination
```

Authentication/CSRF and deployment/coordinator locks stay enabled. No `--no-auth`,
secret options, Podman-qualified coordination, Desktop/Colima, VM/proxy/remote
socket, containerized Brine or cross-host shared-state claim. Linux alone does
not prove a supported topology. Configuration displays **configured only** until
an actual validated receipt supplies qualified identity; static discovery is not
readiness. `endpointDigest` identifies only the normalized requested selector,
not a daemon. `qualifiedIdentityDigest` is a separate received identity token.

## Routes, restrictions and receipts

Configured manual stack **up/start/restart/stop/down** negotiate
`operation-capabilities --format json`, then invoke `dill operation <action>
--contract dill.operation/1 --format json` with explicit engine/socket/stack,
all coordination flags and `--coordination-source brine`. Up explicitly uses
`--no-prune --pull never`. No unsupported-version fallback, direct SDK mutation,
reconnect, retry or replay is allowed. `rm` has no reviewed Brine route and remains
deferred; **teardown is unsupported**. Both qualified API stack deletion variants
are denied to preserve source/recovery context. Secret preview/confirm cannot
bypass the qualified client, even if a secret controller were also installed.

Plan and saved-source validation are read-only operation calls, not qualified
mutation sessions, and carry no coordination flags. Plan renders the actual
received summary and opaque resource digests, never invented raw service names
or field differences. Draft/stdin validation and formatting are unsupported;
save source then validate the saved entrypoint. Validation and plan do not approve
source/candidate deployment and are not saved-plan authorization.

**All Alpha background Git auto-deploy and background pull remain disabled**,
including when Runner.Up is available. The syncer gates before reading a stack or
executing Git/Dill. Manual Git pull remains a separate explicit source edit under
the existing lock; it never automatically deploys. Unattended candidate/source
approval belongs to #67. Direct container/image SDK writes remain disabled.

The browser consumes a bounded structured `[brine:operation]` marker containing
validated receipt, process exit code and independent overall invocation status.
It displays operation ID, outcome, effects, fixed errors, endpoint selector digest
and any qualified identity digest using text nodes, not `innerHTML`. Missing,
malformed, duplicate, oversized or unsupported terminal envelopes, unknown effects,
transport disconnect and EOF without a terminal marker never display complete.
Raw subprocess stdout/stderr are captured privately, not rendered in this mode.

A successful no-op may have `succeeded/none`: success does not prove a change.
A post-commit coordination cleanup error can preserve known **succeeded/complete**
engine axes with **exit 1 / failed invocation**. These must not become a successful
UI completion. Down may be failed/partial after container removal because storage
and networks are conservatively retained, or failed/none for an empty retained
network. Partial/unknown effects never imply rollback. Resource observations are
bounded, may be truncated, and are not a complete recovery inventory.

Only the **current action's** receipt is shown; Brine does not persist full history
or claim it will be available after navigation/restart. Copy the operation ID and
qualified identity before leaving. A disconnect is not permission to reissue an
action. No automatic acknowledgement or journal deletion exists.

## Offline inspection and recovery

Stop and inspect uncertain/partial results independently, including actual live
resources. Use the operator-selected exact candidate, shared state and expected
identity. These are offline shapes with no engine/config evaluation or replay:

```sh
/path/to/b3/dill coordination inspect --state-dir /absolute/private/shared-coordination \
  --daemon-id INDEPENDENTLY_PROVISIONED_EXPECTED_ID --stack EXACT_STACK --timeout 5s

# Only AFTER independent actual-resource inspection and exact record review:
/path/to/b3/dill coordination acknowledge --state-dir /absolute/private/shared-coordination \
  --daemon-id INDEPENDENTLY_PROVISIONED_EXPECTED_ID --stack EXACT_STACK \
  --operation-id op_EXACT_INSPECTED_ID --inspected --timeout 5s
```

Alternatively use the **received qualifiedIdentityDigest** with `--target-id`
instead of `--daemon-id`, never the endpoint selector digest. Wrong-ID, corrupt,
active or changed records cannot be cleared by guessing. Acknowledgement clears
only that exact operation's reservation; it does not replay, roll back or clean up
resources. Never automatically delete, replace or repair journal/key/lock files.
General retention, history, source snapshots and restart-safe approval are not
implemented by this consumer.

## Pilot data restrictions and remaining evidence

Use only prepared local immutable no-VOLUME images (all image-declared VOLUMEs,
even covered ones, are unsupported), effective pull never, synthetic disposable
data and dedicated stack-named volumes. No production secrets/data, bind mounts,
external/anonymous/custom storage, privilege/devices/added capabilities/special
namespaces, orphans, shared image deletion or valuable workloads. Existing source
and ownership guards remain; the consumer is not a sandbox for arbitrary Pkl.

Tests cover synthetic API boundaries, producer-shaped positive receipt/plan UI,
malformed/unsupported terminals, no-op and cleanup failure, and explicit background
gates. They do not qualify the app, filesystem, host topology or Docker lifecycle.
Native application/browser canary and independent final-head review remain pending.
