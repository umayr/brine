# Released Dill integration tests

`TestReleasedDill` runs Brine's local Dill runner against v0.8.0, the release bundled in the Brine image. It does not start Brine or connect to a container engine. The test is opt-in: `go test ./...` skips it unless `BRINE_TEST_DILL` is set. Once enabled, missing prerequisites and a mismatched release version are errors.

## Setup

Use the Go version in `go.mod` and install [Pkl 0.31.1](https://github.com/apple/pkl/releases/tag/0.31.1), the version bundled in Brine. The test checks the Pkl executable on `PATH` or uses `DILL_PKL_PATH` if set. It passes the resolved path to Dill so the test does not use Dill's downloader or cache.

Download the matching binary and schema archive from the [Dill v0.8.0 release](https://github.com/umayr/dill/releases/tag/v0.8.0). For macOS arm64:

```sh
mkdir -p /tmp/brine-dill-test
cd /tmp/brine-dill-test
curl -fL -o dill https://github.com/umayr/dill/releases/download/v0.8.0/dill-darwin-arm64
curl -fL -o schema.zip 'https://github.com/umayr/dill/releases/download/v0.8.0/dill%400.8.0.zip'
shasum -a 256 dill schema.zip
# Compare both digests below before running Dill or extracting the schema.
chmod +x dill
./dill version  # v0.8.0
unzip -p schema.zip dill.pkl > dill.pkl
```

Use the binary and SHA-256 digest for your platform:

| Release asset | SHA-256 |
| --- | --- |
| `dill-darwin-arm64` | `16ea97d6514bf99ca632d515beabf906793489ae8bbcf15128fdae998227ce42` |
| `dill-darwin-amd64` | `ef16d200e701b4d2c4c3701b743d2f7ca175c81d67f1654e938e8b677ad7431b` |
| `dill-linux-amd64` | `71858cada246f3af21f0c29101db9e801d88ab27a05758c3c9f37182e0fe4aee` |
| `dill-linux-arm64` | `76f0537bc18c8588d1c54b61dc874eecfe39972ad214c1638861ea469f0f27d9` |
| `dill@0.8.0.zip` | `47a8dbdc127e947238308cb7a863122e6de128ee6d2e893c677d3117f36f0948` |

From the Brine checkout, run:

```sh
BRINE_TEST_DILL=/tmp/brine-dill-test/dill \
BRINE_TEST_DILL_SCHEMA=/tmp/brine-dill-test/dill.pkl \
go test -count=1 -v ./internal/dill -run '^TestReleasedDill$'
```

The test checks the reported version, not the asset digest. It imports a temporary copy of the release schema locally and does not fetch Pkl packages, pull images, or contact a live socket.

## Coverage and limits

The suite uses the same runner methods as Brine for file validation, stdin validation, and formatting. It checks explicit socket selection using a nonexistent socket, rejection when no socket is configured, private runtime HOME, relative imports, invalid Pkl and schema types, and formatting without modifying the source file. For malformed Pkl, it also checks Dill's exit code `1` and JSON `evaluator_failure` separately from the diagnostic Brine returns. It does not test plan, lifecycle operations, or the web/API layers.
