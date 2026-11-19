# Qualified Alpha operation consumer — b3 compatibility and accepted successor

> **Historical specification:** Alpha and strict-secret functionality is disabled in Brine. This document is retained for design and recovery context, not as operating instructions. Existing secret state and downgrade protections must not be removed or bypassed.
>
> **Current CLI:** Normal server startup rejects `--profile alpha` and explicitly supplied `--coordination-*`, `--operation-state-dir`, and `--secret-*` options, including empty values. Examples and operational claims below describe the historical contract, not currently available behavior.

This implements Brine #66's bounded **attended CLI consumer**, not #67 durable
review/source approval, #68 deployment security qualification or #69 native
application/browser acceptance. **Actual native Brine + Dill b3 + browser canary
has NOT RUN; qualification remains pending.** Synthetic tests are not engine or
host evidence. No production use, host provisioning or resource creation is
performed by this integration.

Optional [S1 private request history](alpha-operation-history.md) retains the existing
mutation stream and adds authenticated GET by Brine request ID. It requires explicit
`--operation-state-dir`, is source **UNBOUND**, and does not expand these qualification
claims or enable any action. Outside that opt-in the behavior below is unchanged.

## Accepted successor and compatibility boundary

The accepted corrected provider successor is Dill main
`2a725aa76f6cbca3bfacd480137b7d183b326ce0` (tree
`504d09d2336b84c2272cdaebe48dae3daf71feb3`). Both supervisors independently
received the actual main artifacts against exact-main CI `43683773067` and
one disposable qualification run `43684062769`, and compared all 20 input-pin
fields. Docker passed all 30 required Dill-side cases; Podman remains unqualified
(11 passed / 19 blocked). This is finite same-profile provider acceptance,
**not Brine source or application qualification**, receiving-daemon identity or
universal mixed-port convergence assurance.

Exact Linux amd64 accepted supply:

- Docker artifact `14149539591`, `79944927` bytes, ZIP SHA256
  `1219db85bb08e0b92102f27c7428e38170271665276a046dc650150d1143d715`,
  official expiry `2026-11-29T06:17:51Z`.
- Binary SHA256 `7af78b677cde2221dfa81f90b98ebed83b09a4bc187c57b619747a8f8d640709`.
- Handoff v2 tar SHA256
  `726ce9a24b0a0b278440c08677670a91bbaec8f9d0aedb01c4f3139875c0e3e3`;
  manifest SHA256
  `59f4e9f413e77af61a92d8bdf1a96ffc62ce6be7767b96c225da827aac1f4cec`.

The corrected source adds only `test/integration/port_bindings_test.go` to the
closed outer artifact namespace (55 to 56 files). The 32 canonical public
handoff members, catalog of 30 cases, input's 20-key schema and qualified profile
are unchanged. Pkl remains 0.31.1 with Linux amd64 SHA256
`618f13955d755cafbfe8c9cba1d27635848cd49dbc6abffd398d2751db1231bf`;
schema SHA256 remains
`b34206d0622fc6c1aa99d5edefb9b6d4b01232635756d2c67fb135161f8db345`.

The pinned synthetic CI matrix preserves `legacy-b3` and replaces the previous
successor selector with closed `preflight-main-2a725`. The old
`preflight-main-2d48` selector and raw source SHAs are rejected, not aliases or
additional accepted candidates. Each builds its exact clean source commit/tree
and checks the same unchanged operation fixtures. Only the successor also
checks actual offline preflight discovery against the frozen wire object.
These are **source-built read-only synthetic compatibility tests**, not execution
of the accepted artifact, native engine qualification, source-boundary proof or
permission to enable the dormant attended controller. Unknown selectors fail;
neither branch names nor a same-tree PR commit substitute for the main pin.
The separate Dill input preparer must explicitly support evidence/handoff v2;
the old b3/v1 entrypoint is not silently reinterpreted or switched.

No artifact is automatically downloaded or installed by Brine. Artifact expiry
is an input-receiving bound, not an invitation to use a different or latest run.
The qualification did not change the then-bundled Dill v0.7.0 or the legacy secret CI pins.
Source execution and actual app qualification stay disabled pending their own
evidence and integration; the original Pkl audit is deferred, not waived.

## Historical 2d48 receipt, not a current promotion pin

The earlier main `2d48de89947c1bec0c4e305b6d05e9172cfdaecd` (tree
`5a4b96b8410be7f852bfcb09f80ddb48469c72e2`) was received against CI
`43666038516` and qualification `43666204662`. Preserve its historical Docker
artifact `14143195067`, ZIP SHA256
`3cbf172ed1b5e802ebe615bba38a9022ee9b17849824357a21831df0386b43bc`,
expiry `2026-11-29T02:16:09Z`, binary SHA256
`5338c665f0ca710dd6fbc3f0da71db836d73ad9954ff9880a593406e203b72d7`,
handoff tar SHA256
`274620193c7d71cf391f9f064ad539ae3ec0ab8edef299ef7ccd07321e3f579c`,
and manifest SHA256
`c63c895d968db91432199d12b2617bac5c36b97b08007a995cd9c1fb44db96d6`.
That receipt does not waive the subsequently confirmed private HostIP
`0.0.0.0`/`::` alias ambiguity; native stale-apply impact was not demonstrated.
The finite corrected repair and newly received actual-main artifacts supersede
that promotion hold only for exact `2a725`, not an arbitrary latest successor.
Neither this history nor driver-branch CI artifacts replace the accepted supply.

## Historical b3 candidate and trust boundary

The prior reviewed Dill candidate is main
`b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2` (tree
`cc545129c5a178cd07fa97b9e7827f74fe624866`). The Linux amd64 binary SHA256 is
`3acea65d4121402194787bb0bdccc20a79c725e1ee5bb2337cc5b33b16e12d51`;
public handoff tar SHA256 is
`75a108b6537457b36c4b6aa75f25895683e60d45e104c6fe8085016584a34bcd`.
Use externally reviewed provenance/checksums before extracting/executing artifacts;
an archive's own manifest cannot authenticate itself. Brine does not download,
install or select a new candidate automatically. Choose its executable explicitly
with `--dill /absolute/operator-installed/b3/dill`. The then-bundled **Dill v0.7.0 was incompatible with this operation contract**. Version
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
