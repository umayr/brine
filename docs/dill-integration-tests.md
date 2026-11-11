# Released Dill integration tests

`internal/dill/runner_integration_test.go` is opt-in. It invokes the actual Dill
v0.5.0 CLI through Brine's `dill.Runner`; it does not launch Brine or contact a
container engine. Normal `go test ./...` skips it unless `BRINE_TEST_DILL` is
set. Once opted in, missing prerequisites, an unexpected version, or a test
failure are errors, not skips. Use `-count=1` to avoid cached results.

## Prerequisites and acquisition

You need Go (the version in `go.mod`), `curl`, `unzip`, and Pkl. Dill must be
able to find Pkl on `PATH`, or via `DILL_PKL_PATH`. Brine's container uses Pkl
0.31.1; the test has also been run with 0.32.2. Install a release appropriate to
your platform from https://github.com/apple/pkl/releases and check `pkl --version`.

Download the executable and schema archive directly from the [Dill v0.5.0
release](https://github.com/umayr/dill/releases/tag/v0.5.0). For example, on
macOS arm64 (replace the asset name and digest for your OS/architecture):

```sh
mkdir -p /tmp/brine-dill-test
cd /tmp/brine-dill-test
curl -fL -o dill https://github.com/umayr/dill/releases/download/v0.5.0/dill-darwin-arm64
curl -fL -o schema.zip 'https://github.com/umayr/dill/releases/download/v0.5.0/dill%400.5.0.zip'
shasum -a 256 dill schema.zip
# Compare both digests to the values below before executing or extracting.
chmod +x dill
./dill version                         # v0.5.0
unzip -p schema.zip dill.pkl > dill.pkl
```

SHA-256 digests of the v0.5.0 release assets (also available in the GitHub
release API's asset `digest` fields):

| Asset | SHA-256 |
| --- | --- |
| dill-darwin-arm64 | `99ce1fabc14cf880037d03c7ad43a7eff3f48a2d8435060210bd41cbf40481eb` |
| dill-darwin-amd64 | `dc648a348b976e0064cfd4fa4cb3d1bd8b7dfbc9a2d65ed130eedc4e5c70d355` |
| dill-linux-amd64 | `37d087f8e8ad38338c024f5c65a92272d04d2b5412760f2a9e6823523357a059` |
| dill-linux-arm64 | `8defbe937769347170a33dc9fc7ddd0be602efcfc923faba5a2036754fab4ed4` |
| dill@0.5.0.zip | `b190b8ada4372be7704dfdac8b0021eea9eb9b6deb29efe10b8a6a606b730edf` |

From the Brine checkout, using absolute paths:

```sh
BRINE_TEST_DILL=/tmp/brine-dill-test/dill \
BRINE_TEST_DILL_SCHEMA=/tmp/brine-dill-test/dill.pkl \
go test -count=1 -v ./internal/dill -run '^TestReleasedDill050$'
```

The test checks the CLI's reported version, not its provenance: verify the
asset digest yourself. It copies the supplied release schema to a temporary
fixture directory, so Pkl resolves a local import rather than fetching the
published package. The fixture uses an image name only as configuration; it
never pulls or runs that image. No production engine or service endpoint is
needed. Downloads above are setup steps, not part of the test.

## Coverage and limits

The suite checks file and stdin validation of valid and invalid Pkl, relative
schema resolution for stdin, formatting and formatting idempotence, and that
stdin operations do not overwrite the original file. Invalid validation tests
cover Dill's JSON validity response rather than relying on its exit status.
It does not test `plan`, `ps`, or lifecycle operations: those require a real
container engine and are deliberately not run against an existing endpoint.
It also does not exercise network package resolution or the web/API layers.
