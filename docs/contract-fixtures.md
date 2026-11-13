# Pinned operation contract fixtures

These public files are copied **byte-for-byte**, without newline or operation-ID
rewrites, from Dill commit `b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2`
(tree `cc545129c5a178cd07fa97b9e7827f74fe624866`). Source directory:
[`internal/operation/testdata`](https://github.com/umayr/dill/tree/b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2/internal/operation/testdata).
Brine destination: `internal/dill/testdata/operation-v1/`.

The source manifest version is `dill.operation-fixtures/1`; the CLI contract is
`dill.operation/1`. The manifest and every public operation fixture are retained,
including the two qualified fixtures. The manifest's `runtime` values select
Dill's synthetic test harnesses, **not executable Brine requests**. Fixture
`expectedExit` and `expected` are decoder expectations, **not process evidence**.
The all-zero operation ID is a golden normalization sentinel. Actual invocations
must first validate `^op_[0-9a-f]{32}$` and freshness before normalization.

## SHA-256 manifest

Paths below are relative to `internal/dill/testdata/operation-v1/`.

```text
448d2e331c46cf985e05814bebfe285dd293f7bb4fddd0dc5983f6807cfb1f4e  failure.json
1923e39eb9fedef441f50079f240063144ca3f40c0409cfac57bc04f988d5181  manifest.json
67fd90a487075b76134c3bf6031a17c9ca88a57588a229fbee521f6935217a04  qualified-cleanup-failure.json
6c2f603ee6eb6509dd055b2aebf32304c37ee6a7d6b416c2921cd9c47d9ec955  qualified-success.json
d8fb618b7c1728ba2a2c03c7836fb47a46d414074f3610aad3a1e13b5d7f6c91  success.json
31b22d9377cc397b02ea46a8ff8617a24f8b2fe9e2727f1cf4f96bd359a6c1f1  unsupported-operation.json
2aaf103b10bbddd2b0b34400ce85f29783b459c6a3e4e0bb5fa5aeea23da943d  unsupported-version.json
```

Copied using `git -C /Users/umayr/Code/dill show <exact-commit>:<source-path>`;
not from bundled Dill 0.7.0, a release archive, mutable main, or the older paired
secret candidate. The independent script also compares these exact bytes with
the clean source checkout before building. Pinned schema `dill.pkl` SHA-256:
`b34206d0622fc6c1aa99d5edefb9b6d4b01232635756d2c67fb135161f8db345`.

## Contract and evidence boundaries

Read at the exact commit above: `docs/operation-v1.md`,
`docs/local-mutation-coordination-design.md`,
`docs/alpha-coordination-qualification.md`, and
`docs/alpha-candidate-handoff.md`. The separate handoff was
`/Users/umayr/Code/dill/.pi/handoffs/brine-main-b3-contract-handoff.md`.
No handoff archive/binary was extracted or executed on this Mac.

`operation` is the first CLI token; discovery is `operation-capabilities
--format json`, not metadata RPC, `caps`, `version`, or secret discovery.
A configured candidate uses the operator's explicit `--dill` binary, not a
silent bundled-0.7.0 fallback. The ordinary supported result and discovery
fixtures exercise the consumer decoder; real process tests must separately
execute discovery, offline validation, and read-only plan/diff over a synthetic
Unix Docker SDK HTTP server that rejects every non-read request. They must not
use real HOME, credentials, Docker sockets, production or homelab resources.

Outcome, effects, fixed errors **and process exit** are independent evidence:
plan changes exits 1, diff changes exits 0; declined confirmation may exit 0
with cancelled/none. `qualified-cleanup-failure.json` is deliberately
exit 1 / succeeded / complete plus `COORDINATION_RELEASE_FAILED`, never overall
success. Missing/malformed/incomplete terminal output is unknown. Resources are
bounded observations, not a complete recovery inventory or rollback guarantee.

The bounded Linux CI job is **not a native-engine qualification**: it only
builds exact clean b3 through Dill's Makefile with its required build tags,
checks schema/fixture provenance, and exercises a read-only synthetic endpoint.
On an explicitly isolated disposable Linux executor, run:

```sh
BRINE_OPERATION_ISOLATED=1 \
BRINE_OPERATION_CANDIDATE_REPO=/absolute/clean/exact-b3-checkout \
  bash scripts/ci-operation-contract.sh
```

The script verifies the binary's embedded clean VCS revision, downloads the Pkl
version/hash specified by that source, and rejects a skipped/missing selected
positive test. It does not execute a prebuilt handoff payload. Process tests use
the public Alpha `Runner.Plan` / `Runner.Validate` paths with a synthetic
configured-only state assertion; read-only requests omit coordination flags and
must leave that state directory empty. They compare actual validation and
negotiation process results to the four ordinary goldens. The two qualified
goldens remain decoder-only checks, **not qualified mutation process evidence**.
Static Linux qualification discovery is not host attestation; non-Linux hosts
must omit the qualified feature and all three qualified arrays. Qualified engine
mutations, actual Brine app/browser integration, native canary, lifecycle/restart
and recovery acceptance remain **NOT RUN** by this lane. Dill-side native Linux
Docker evidence does not change that boundary. The older `258e73e` paired-secret
workflow is a separate compatibility lane and remains unchanged.
