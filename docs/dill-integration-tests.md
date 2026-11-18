# Released Dill integration tests

`TestReleasedDill` runs Brine's local Dill runner against v0.7.0, the release bundled in the Brine image. It does not start Brine or connect to a container engine. The test is opt-in: `go test ./...` skips it unless `BRINE_TEST_DILL` is set. Once enabled, missing prerequisites and a mismatched release version are errors.

## Setup

Use the Go version in `go.mod` and install [Pkl 0.31.1](https://github.com/apple/pkl/releases/tag/0.31.1), the version bundled in Brine. The test checks the Pkl executable on `PATH` or uses `DILL_PKL_PATH` if set. It passes the resolved path to Dill so the test does not use Dill's downloader or cache.

Download the matching binary and schema archive from the [Dill v0.7.0 release](https://github.com/umayr/dill/releases/tag/v0.7.0). For macOS arm64:

```sh
mkdir -p /tmp/brine-dill-test
cd /tmp/brine-dill-test
curl -fL -o dill https://github.com/umayr/dill/releases/download/v0.7.0/dill-darwin-arm64
curl -fL -o schema.zip 'https://github.com/umayr/dill/releases/download/v0.7.0/dill%400.7.0.zip'
shasum -a 256 dill schema.zip
# Compare both digests below before running Dill or extracting the schema.
chmod +x dill
./dill version  # v0.7.0
unzip -p schema.zip dill.pkl > dill.pkl
```

Use the binary and SHA-256 digest for your platform:

| Release asset | SHA-256 |
| --- | --- |
| `dill-darwin-arm64` | `0fdfbc9530af21b2f91b4dee9a95fd5d02dd2e280f95c8145450827ac06d5e7a` |
| `dill-darwin-amd64` | `833053fc5009f0d293d092c5f5c50d773f3b9fcefd11593de234beaa2f96f4cb` |
| `dill-linux-amd64` | `51ad9b623fdb2f255c280dd556ec7f3f77da55f1a4c266b74cd4d664f6088263` |
| `dill-linux-arm64` | `836f7c32a4e1353b94c2be7b2a0e2ba7ab556fd2bee8b275ea9abfaa23a37925` |
| `dill@0.7.0.zip` | `47a8dbdc127e947238308cb7a863122e6de128ee6d2e893c677d3117f36f0948` |

From the Brine checkout, run:

```sh
BRINE_TEST_DILL=/tmp/brine-dill-test/dill \
BRINE_TEST_DILL_SCHEMA=/tmp/brine-dill-test/dill.pkl \
go test -count=1 -v ./internal/dill -run '^TestReleasedDill$'
```

The test checks the reported version, not the asset digest. It imports a temporary copy of the release schema locally and does not fetch Pkl packages, pull images, or contact a live socket.

## Coverage and limits

The suite uses the same runner methods as Brine for file validation, stdin validation, and formatting. It checks explicit socket selection using a nonexistent socket, rejection when no socket is configured, private runtime HOME, relative imports, invalid Pkl and schema types, and formatting without modifying the source file. For malformed Pkl, it also checks Dill's exit code `1` and JSON `evaluator_failure` separately from the diagnostic Brine returns. It does not test plan, lifecycle operations, or the web/API layers.
