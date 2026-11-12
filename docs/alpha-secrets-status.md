# Alpha file secrets: implementation status

Brine's bundled Dill 0.5.0 does **not** support the Alpha file-secret contract.
The reference editor, reviewed rotation and secret-enabled deployment workflow
are not available in this build. Do not use this build to deploy secret-enabled
recipes. Capability discovery in the source tree is a foundation, not an enabled
feature or a readiness check. The reference UI is not yet available and paired
Dill/runtime qualification is pending; do not use the implementation for
secret-enabled deployments yet.

Supplying both `--secret-target` and an absolute `--secret-policy`, together
with an explicit local `--engine-socket`, selects strict mode. In strict mode
all stack plan, up, start and restart operations require exact Alpha capability
discovery, including for unmarked stacks; an old or malformed Dill receives an
upgrade error. Optional `--secret-pkl` selects an absolute operator-installed
Pkl binary. The policy must be outside Brine's data directory. Disabling these
options preserves legacy compatibility, but is not Alpha secret support or a
secret-safety guarantee; known secret-enabled deployment records and container
markers still prohibit downgrade. Stop/down/teardown do not require capability
discovery or source availability. Dill subprocesses receive a minimal explicit
environment rather than the Brine server environment.

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
