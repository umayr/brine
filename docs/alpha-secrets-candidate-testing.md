# Paired candidate checks (pending candidate pin)

The bundled Dill 0.5.0 is incompatible. Do not bump it or claim released
support. The CI Docker job tests that its capability command fails. The Go
candidate test is opt-in and has **not** qualified either engine.

On an isolated disposable Linux engine, build Dill with that candidate's
`make build`, and use the candidate's own Pkl schema in a synthetic config.
Provision a dedicated policy, socket, target, stack and read-only synthetic host
file externally, under the contract's protected filesystem and ownership rules.
Never use production secrets or engines. Record the exact candidate commit,
architecture, binary SHA-256 and schema provenance. Verify the transferred
artifact checksum independently before running:

```sh
BRINE_DILL_CANDIDATE=/absolute/bin/dill \
BRINE_DILL_CANDIDATE_COMMIT=<40-lowercase-hex-commit> \
BRINE_DILL_CANDIDATE_REPO=/absolute/clean/candidate/checkout \
BRINE_DILL_CANDIDATE_SHA256=<64-lowercase-hex-checksum> \
BRINE_DILL_PKL=/absolute/bin/pkl \
BRINE_DILL_SCHEMA=/absolute/candidate/dill.pkl \
BRINE_DILL_CONFIG=/absolute/synthetic/compose.pkl \
BRINE_DILL_POLICY=/absolute/protected/policy.json \
BRINE_DILL_BINDINGS=/absolute/synthetic/bindings.json \
BRINE_DILL_SOCKET=/absolute/local/engine.sock \
BRINE_DILL_ENGINE=docker \
BRINE_DILL_TARGET=lab-a BRINE_DILL_STACK=demo \
go test -count=1 -run TestCandidateDillSecretReadOnlyIntegration ./internal/dill
```

Use `podman` with a separately provisioned disposable rootless runner for its
job. The harness performs discovery, status and plan only: it does not apply or
prove workload reads, rotation, namespace mapping, mount enforcement or cleanup.
The fixture config must explicitly import the schema using its absolute
`file://` URI. The harness checks the checkout commit, tracked cleanliness and
schema against that commit; it cannot establish the binary's build provenance
from its checksum alone. Final paired CI must
pin that commit and artifact checksum and run the contract's full engine and
browser matrix. No checksum or candidate is pinned until coordination completes.

## Optional disposable mutation harness

`TestCandidateDillSecretDisposableMutation` is **separately opt-in** and must
only be run on a fresh disposable Linux Docker or rootless-Podman engine. It
creates a uniquely named `brine-ci-*` stack and private synthetic workspace,
policy, ownership marker and two source files beneath a freshly created private
home-directory fixture. Its image must already be present and run continuously;
no image is pulled by the fixture. Grant UID/GID must match the selected engine's
namespace mapping to the current fixture owner's host UID/GID.

```sh
BRINE_DILL_DISPOSABLE_MUTATION=I_HAVE_A_DISPOSABLE_LINUX_ENGINE \
BRINE_DILL_CANDIDATE=/absolute/bin/dill \
BRINE_DILL_CANDIDATE_COMMIT=<40-lowercase-hex-commit> \
BRINE_DILL_CANDIDATE_REPO=/absolute/clean/candidate/checkout \
BRINE_DILL_CANDIDATE_SHA256=<64-lowercase-hex-checksum> \
BRINE_DILL_PKL=/absolute/bin/pkl BRINE_DILL_SCHEMA=/absolute/candidate/dill.pkl \
BRINE_DILL_SOCKET=/absolute/disposable/engine.sock BRINE_DILL_ENGINE=docker \
BRINE_DILL_TEST_IMAGE=<preloaded-long-running-fixture-image> \
BRINE_DILL_TEST_GRANT_UID=<container-uid> BRINE_DILL_TEST_GRANT_GID=<container-gid> \
go test -count=1 -run TestCandidateDillSecretDisposableMutation ./internal/dill
```

It exercises checked application and rotation, read-only host mount inspection,
stale config/import/policy/live-state confirmation, and cleanup after deliberately
renaming its own synthetic source directory to simulate external disappearance.
The directory is restored even on failure; source bytes and modes are checked
before test fixture disposal. This deliberate test fault injection is not product
behavior. A failed test may leave disposable engine resources requiring CI
cleanup; it must never run against a shared or production engine. Passing this
harness is not a substitute for workload-read, namespace/security, browser and
full paired Linux qualification. No candidate has yet been pinned or tested.

## Legacy lifecycle compatibility

Brine sends its trusted stack identity, selected engine and the exact effective
local socket used for inventory to every Dill lifecycle invocation, including
cleanup. If no socket flag is supplied in legacy mode, Brine resolves the
inventory endpoint once at startup and reuses that same endpoint. Strict mode
still requires an explicitly configured socket. An old Dill that cannot accept
endpoint pinning is rejected, including for stop/down/teardown; Brine will not
fall back to an ambient endpoint or report cleanup as successful. Read-only
inventory does not require the Dill binary. Install a compatible endpoint-pinning
Dill to perform lifecycle operations; this is not a claim that bundled 0.5.0 is
Alpha-compatible.

The paired candidate must also support Brine's explicit operator-selected
`--secret-target` flag. Brine supplies it to secret plan, check, apply and
strict validation/lifecycle invocations; the binding's `target` must match and
never selects authority. Cleanup does not require the selector, bindings or
policy. Selector presence alone must not require bindings for an ordinary
configuration with no grants. Neither harness uses `DILL_SECRET_TARGET`.
