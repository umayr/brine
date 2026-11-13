# Alpha file secrets: implementation status

Brine now bundles released Dill v0.7.0, which advertises the Alpha file-secret
contract. Dill v0.5.0 remains an incompatible legacy baseline. The reference
editor, review and confirmation paths have bounded paired Docker/rootless-Podman
evidence; full Brine HTTP/browser canary and restart/recovery qualification remain
pending. Capability discovery checks interface compatibility, not target readiness.
Use the matching v0.7.0 schema for secret-enabled recipes. The qualified Linux
profile requires kernel 6.5+; the qualified Podman profile uses Podman 5.0.0 and
crun 1.26. This is not support for arbitrary runtime/platform combinations.
Runtime preflight also requires exact daemon peer identity, pidfd/proc access and
the same filesystem root inode as the daemon. Docker Desktop, VM/proxy indirection
and an ordinary Brine container connected to a host daemon are not established
supported secret-runtime topologies. Bundling a compatible binary and passing
static capability smoke checks does not establish containerized host-file-secret
operation; a supported installation topology needs a real server/engine canary.

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

Paired CI retains the exact Dill candidate commit
`258e73e58ba018931be57172908e8e64f433945e`, records its built binary checksum,
and uses its matching Pkl schema. Released v0.7.0 points to
`0554f6d73cb924b06a47f3a495bcad39a9dcb08c`; both commits have the identical
tracked tree `d6495003e1cfcca3fe9f89e2f10f9125661e7e39`.

[Run 43361791813](https://github.com/umayr/brine/actions/runs/43361791813)
passed the required Brine read-only/mutation harnesses and Dill host-file suite
on Docker and rootless Podman. It tested synthetic PR merge
`ba4555df7389cff2b4add928acdefd142e8818c4`, whose tracked tree matches merged
Brine `90949f095012dcd152202d6a4ff396412c241dc9`; it did not execute that final
commit ID. Neither selected Brine candidate test skipped. The wider Dill suite
has conditional skips; do not claim universal zero skips. These tests include
expected nonzero retained-network cleanup, not complete teardown or volume
retention qualification. They do not establish full server/browser deployment,
Brine-restart recovery, or a production-ready release.

The image CI checks the bundled release's static capability contract and retains
an explicitly built v0.5.0 image for incompatible-binary/API rejection. The paired
engine jobs build the source-equivalent candidate, not the downloaded release
binary; static release smoke checks are not real-engine release-artifact proof.

The approved Alpha design uses externally provisioned immutable read-only host
files on both Docker and Podman; native Podman secrets are deferred. Operator
policy and source files remain outside the Brine workspace. This is a trusted
single-operator design, not a multi-tenant sandbox. No production engine should
be used for testing; paired engine checks require isolated disposable hosts.
