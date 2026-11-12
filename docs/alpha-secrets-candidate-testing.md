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
