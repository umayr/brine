# Alpha file secrets: implementation status

Brine's bundled Dill 0.5.0 does **not** support the Alpha file-secret contract.
The reference editor, review and confirmation paths exist for paired testing,
but the bundled binary cannot run them. Paired Dill/runtime qualification is
pending; do not use this build to deploy secret-enabled recipes. Capability
discovery checks interface compatibility, not target readiness.

Supplying both `--secret-target` and an absolute `--secret-policy`, together
with an explicit local `--engine-socket`, selects strict mode. In strict mode
authentication is required and all stack plan, up, start and restart operations require exact Alpha capability
discovery, including for unmarked stacks; an old or malformed Dill receives an
upgrade error. Optional `--secret-pkl` selects an absolute operator-installed
Pkl binary. The policy must be outside Brine's data directory. Disabling these
options preserves legacy compatibility, but is not Alpha secret support or a
secret-safety guarantee; known secret-enabled deployment records and container
markers still prohibit downgrade. Stop/down/teardown do not require capability
discovery or secret-source availability. Stop and down do not read the recipe;
teardown requires a safe, readable recipe for image-selection semantics and is
rejected before invoking Dill if the recipe is missing or unsafe. Dill subprocesses
receive a minimal explicit environment rather than the Brine server environment.

Paired CI is configured to build Dill at the exact candidate commit
`ffbca6c3c2aa36e0dadedecff986c723589b0a2a`, record the built binary checksum,
and use its matching Pkl schema. The candidate must be published before that
checkout can succeed, and the jobs have not yet qualified either engine. This
interim candidate includes the statically reviewed proof fixes and a source-pinned
crun compatibility correction for the observed Podman OCI-version failure.
Fresh native-runtime and paired Linux results remain pending. CI
separately tests the unmodified 0.5.0 image as an incompatible negative case.
No independently pinned binary checksum or compatible released Dill version is
claimed here.

The approved Alpha design uses externally provisioned immutable read-only host
files on both Docker and Podman; native Podman secrets are deferred. Operator
policy and source files remain outside the Brine workspace. This is a trusted
single-operator design, not a multi-tenant sandbox. No production engine should
be used for testing; paired engine checks require isolated disposable hosts.
