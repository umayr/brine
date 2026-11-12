# Alpha file secrets: implementation status

Brine's bundled Dill 0.5.0 does **not** support the Alpha file-secret contract.
The reference editor, reviewed rotation and secret-enabled deployment workflow
are not available in this build. Do not use this build to deploy secret-enabled
recipes. Capability discovery in the source tree is a foundation, not an enabled
feature or a readiness check. In particular, existing lifecycle routes are not
secret-aware and do not yet enforce the required approval or start/restart gate.
The preview-proof interface has been agreed, but its Dill implementation and
Brine confirmation flow are still pending.

The paired Dill candidate is not yet available or pinned. Once coordinated,
paired CI must build Dill with `make build` at the exact candidate commit,
verify the transferred binary's checksum, use its matching Pkl schema and select
it via Brine's `--dill` option. CI must separately test the unmodified 0.5.0
image as an incompatible negative case. No compatible released Dill version or
candidate checksum is claimed here.

The approved Alpha design uses externally provisioned immutable read-only host
files on both Docker and Podman; native Podman secrets are deferred. Operator
policy and source files remain outside the Brine workspace. This is a trusted
single-operator design, not a multi-tenant sandbox. No production engine should
be used for testing; paired engine checks require isolated disposable hosts.
