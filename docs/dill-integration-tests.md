# Released Dill integration tests

`TestReleasedDill` runs Brine's local Dill runner against v0.13.1, the release bundled in the Brine image. It does not start Brine or connect to a container engine. The test is opt-in: `go test ./...` skips it unless `BRINE_TEST_DILL` is set. Once enabled, missing prerequisites and a mismatched release version are errors.

## Setup

Use the Go version in `go.mod` and install [Pkl 0.31.1](https://github.com/apple/pkl/releases/tag/0.31.1), the version bundled in Brine. The test checks the Pkl executable on `PATH` or uses `DILL_PKL_PATH` if set. It passes the resolved path to Dill so the test does not use Dill's downloader or cache.

Download the matching binary and schema archive from the [Dill v0.13.1 release](https://github.com/umayr/dill/releases/tag/v0.13.1). For macOS arm64:

```sh
mkdir -p /tmp/brine-dill-test
cd /tmp/brine-dill-test
curl -fL -o dill https://github.com/umayr/dill/releases/download/v0.13.1/dill-darwin-arm64
curl -fL -o schema.zip 'https://github.com/umayr/dill/releases/download/v0.13.1/dill%400.13.1.zip'
shasum -a 256 dill schema.zip
# Compare both digests below before running Dill or extracting the schema.
chmod +x dill
./dill version  # v0.13.1
unzip -p schema.zip dill.pkl > dill.pkl
```

Use the binary and SHA-256 digest for your platform:

| Release asset | SHA-256 |
| --- | --- |
| `dill-darwin-arm64` | `843f786504f3e26df7bca1bb908c569b0c19ed439380c3d0bd2bfdd1fa98e087` |
| `dill-darwin-amd64` | `6fc5651fe45bbd3df7ed8188cca6698bc92c6828d18e94c10f275446bfbe562c` |
| `dill-linux-amd64` | `a792cf6095d106747cd3bb7b471205ec8f9a48087eae42114a2655d20355d902` |
| `dill-linux-arm64` | `b5b95af40a958dd075cbc672b3a1d968d8ab37591faf0813878c2b506ded90ec` |
| `dill@0.13.1.zip` | `2f2c1c622b9aa7f386c7d401bd912abe289a68554e1974da1bde0b6cceb6b413` |

From the Brine checkout, run:

```sh
BRINE_TEST_DILL=/tmp/brine-dill-test/dill \
BRINE_TEST_DILL_SCHEMA=/tmp/brine-dill-test/dill.pkl \
go test -count=1 -v ./internal/dill -run '^TestReleasedDill$'
```

The test checks the reported version, not the asset digest. It imports a temporary copy of the release schema locally and does not fetch Pkl packages, pull images, or contact a live socket.

## Coverage and limits

The suite uses the same runner methods as Brine for file validation, stdin validation, and formatting. It checks explicit socket selection using a nonexistent socket, rejection when no socket is configured, private runtime HOME, relative imports, invalid Pkl and schema types, and formatting without modifying the source file. For malformed Pkl, it also checks Dill's exit code `1` and JSON `evaluator_failure` separately from the diagnostic Brine returns. It does not test plan, lifecycle operations, or the web/API layers.
