# Paired candidate checks (qualification pending)

The bundled Dill 0.5.0 is incompatible. Do not bump it or claim released
support. The CI Docker job tests that its capability command fails. The paired CI
configuration pins Dill commit `f5c8dff23d0b3a8e43820fcda91d42e253a9cb33`
and builds it from source; the resulting binary SHA-256 is recorded and checked
within each job. The jobs have **not yet run** and do not establish engine support. This is an
interim pin: it contains fixes for the source-identity proof and proof-output
workspace-boundary findings, with independent re-review and fresh Linux results
still pending. The previous candidate passed Docker qualification but failed
both Podman create tests. The source-replacement assertion must not be weakened
to accommodate any candidate.

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
from its checksum alone. The CI job records the locally built artifact checksum and verifies it in both
harnesses; this is not an independently pinned prebuilt artifact checksum. The
Dill commit must be published before the exact-SHA checkout can succeed.

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
full paired Linux qualification. The pinned candidate has not yet been tested
by these Brine CI jobs.

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

### Recipe unavailable or unsafe

Stack metadata remains discoverable if the workspace or configured entrypoint is
missing or unsafe. Editor repair, safe entrypoint selection and Git recovery
remain available when their own workspace prerequisites are safe. Stop and down
use a guaranteed nonexistent config path inside a private invocation directory,
with the trusted stack, engine and socket, so they do not read the unavailable
recipe or an ambient default. Avoiding Pkl discovery and evaluation additionally
requires the paired Dill label-only missing-file fast-path; this is not yet
qualified. The disposable candidate harness checks stop/down with an unavailable
Pkl executable and an evaluation marker. Teardown retains its distinct image-selection
semantics and requires a safe, readable recipe; Brine rejects it before invoking
Dill when that recipe is unavailable or unsafe. This is not a claim of
recipe-independent full teardown qualification.


## Paired CI boundary and coverage

The `paired-alpha` CI matrix uses disposable GitHub Ubuntu Linux Docker and
rootless Podman jobs only. It checks out Dill by the exact commit above. The
Podman job runs that checkout's source-pinned Podman 5.0.0 build script. The
Brine wrapper sources the pinned Dill `ci-alpha-secrets.sh` script, retaining
its EXIT trap, so Dill's own exact-peer SO_PEERPIDFD, protected configuration,
non-root UID/GID mapping and real host-file qualification runs on the dedicated
daemon before the Brine harnesses run against that same live socket. The
wrapper separately proves the namespace maps container UID/GID 0 to its test
fixture owner. It records the built candidate checksum, and explicitly fails
if either selected Brine test skips. No local or production engine is used.

Coverage is layered, not a release claim. The pinned Dill suite exercises real
non-root mapped reads, read-only mount behavior, foreign-UID denial, rotation,
source fault and pre-mutation cases, marker/lifecycle and proof behavior. The
Brine real-candidate harnesses cover read-only status/preview, checked apply and
rotation with mount inspection, stale config/import/binding/policy/source-identity/live-state
confirmation, and source-disappearance stop/down with a Pkl execution marker.
Brine API callback failure/status and marked legacy-downgrade paths currently
have synthetic unit/browser tests only, not a paired real-engine API/browser
test. Brine's own harness does not independently prove workload reads, foreign
UID denial, or Docker/Podman namespace enforcement; those are exercised by the
separate Dill suite. Missing/symlinked recipe route behavior is also synthetic;
the paired stop/down checks exercise a missing secret source, not the full HTTP
route with an unsafe recipe. The mutation harness uses container UID/GID 0
mapped to its own synthetic source owner; the Dill suite separately tests
non-root mapped ownership. These gaps and the exact job results require review
before any support claim.
