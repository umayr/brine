# Inactive v2 attended history seam

> **Historical specification:** Alpha and strict-secret functionality is disabled in Brine. This document is retained for design and recovery context, not as operating instructions. Existing secret state and downgrade protections must not be removed or bypassed.
>
> **Current CLI:** Normal server startup rejects `--profile alpha` and explicitly supplied `--coordination-*`, `--operation-state-dir`, and `--secret-*` options, including empty values. Examples and operational claims below describe the historical contract, not currently available behavior.

This package-level store seam is not wired to an API, runner, or source policy and
**does not activate a profile or qualify a runtime**. It accepts trusted producer
assertions, not arbitrary HTTP JSON as authority. Independent integration must
resolve and validate the opaque references and actual runtime evidence before use.
No native application, compiler, evaluator, engine, provider or source execution
qualification is claimed. The source prototype remains held and runtime audit is
blocked; no provider filter is bypassed.

`SourceRef` has three separate SHA-256-shaped opaque **recorded assertions**:
`SnapshotDigest` commits to a captured external-workspace manifest, entrypoint and
bytes, not hermetic evaluator output; `EvaluatorProfileDigest` commits to a closed
policy descriptor including approved trusted-interpreter model, fixed settings,
schema and flag policy; `EvaluatorRefDigest` refers to the exact pinned evaluator
and launcher plus qualification descriptor, never a filesystem path. A digest's
syntax is not evidence that any of those claims is true. The future trusted runtime
controller must independently resolve and verify the descriptor and evidence.
Audited builtins, derived virtual modules and runtime facts may exist; there is no
physical-only or hermetic claim. External resource, network, project and package
authority remain denied. Expected target is independently provisioned context,
not an identity learned from the selected socket.

Version `brine.operation/2` has a separate 512 KiB maximum. The actual preflight
result has a 262144-byte producer bound and at most 256 rows. A complete successful
preview is recorded once, with validated literal resource names and IDs retained as
private operational metadata, including all rows. These strings may themselves
contain operator-chosen sensitive text: the format cannot guarantee otherwise. They
are not public artifacts; any future authenticated lookup must apply an explicit
privacy review. No raw source, environment, settings, host paths, credentials or
raw process output belongs in these records. Fixed catalog diagnostics accepted
by the applicable Dill decoder are retained; arbitrary/freeform diagnostics are
not. The preflight parser enforces closed shapes, literal bounded names and
identities and the producer's resource matrix.

Apply receives a distinct Brine request ID and references the durable successful
preview ID and a domain-separated digest of the canonical immutable intent and
validated preflight result. The digest is distinct from provider input/selection
commitments and is neither authentication nor execution authority. Actor, action,
stack, all three source assertions and expected target must match. A durable apply
intent consumes its preview exactly once, including after failed or interrupted
apply and restart; reuse requires a new preview, never an automatic retry. Actual
target, input and selection are separately present in the preflight result. The store does
not infer confirmation, execute, replay or grant permission. Dill IDs occur only in
validated producer invocations, not invented from Brine IDs.

Intent is published before the caller may launch any child. A terminal marker must
be suppressed unless the terminal update succeeds. A persistence failure poisons
the store and retains known typed observations in process memory marked
`failed_inspect`, never durable. Restart changes unfinished records to unknown
with a recovery observation, without an invented completion time, replay, PID action,
coordination journal inspection or acknowledgement. Delivery is a local write
observation, never browser acknowledgement. Filesystem ownership, root exclusion,
ACL, descriptor, lock and atomic fsync rules are inherited from S1.

The original `brine.operation/1` remains strictly UNBOUND with its unchanged 3 MiB
limit and closed canonical shape; it is never upgraded or interpreted as bound.
The in-process `Lookup` helper dispatches once by the explicit stored version and
returns one typed record, including any known nondurable failure overlay. A future
authenticated lookup must serialize that selected original record, not the helper
wrapper, and must never fall back to another decoder after corruption.
The v2 apply path uses the explicit bound operation decoder; v1 keeps its original
legacy decoder. Apply receipt data does not provide an independent coordination
effect, so that axis is unreported rather than inferred. Independent review and all
source/provider/runtime and native-app gates remain required before integration or use.
