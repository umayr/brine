# Released Dill integration tests

`TestReleasedDill` runs Brine's local Dill runner against v0.12.0, the release bundled in the Brine image. It does not start Brine or connect to a container engine. The test is opt-in: `go test ./...` skips it unless `BRINE_TEST_DILL` is set. Once enabled, missing prerequisites and a mismatched release version are errors.

## Setup

Use the Go version in `go.mod` and install [Pkl 0.31.1](https://github.com/apple/pkl/releases/tag/0.31.1), the version bundled in Brine. The test checks the Pkl executable on `PATH` or uses `DILL_PKL_PATH` if set. It passes the resolved path to Dill so the test does not use Dill's downloader or cache.

Download the matching binary and schema archive from the [Dill v0.12.0 release](https://github.com/umayr/dill/releases/tag/v0.12.0). For macOS arm64:

```sh
mkdir -p /tmp/brine-dill-test
cd /tmp/brine-dill-test
curl -fL -o dill https://github.com/umayr/dill/releases/download/v0.12.0/dill-darwin-arm64
curl -fL -o schema.zip 'https://github.com/umayr/dill/releases/download/v0.12.0/dill%400.12.0.zip'
shasum -a 256 dill schema.zip
# Compare both digests below before running Dill or extracting the schema.
chmod +x dill
./dill version  # v0.12.0
unzip -p schema.zip dill.pkl > dill.pkl
```

Use the binary and SHA-256 digest for your platform:

| Release asset | SHA-256 |
| --- | --- |
| `dill-darwin-arm64` | `12c49e01c6d9d7e22672a2026f67d5970cdc3cbeacbf956355599b52708bf34b` |
| `dill-darwin-amd64` | `7f21619679f0722983672fbd17e6b461531d06c94b6709d9ade208be1913a520` |
| `dill-linux-amd64` | `27e4298ccb60609f4405fbcdf387617617c2e4dcd74119191f86062cf791c126` |
| `dill-linux-arm64` | `c1302991a2d38aba39c4fc24f56dd2d5e76ced7956fcd958b4ece108c6666b83` |
| `dill@0.12.0.zip` | `2f2c1c622b9aa7f386c7d401bd912abe289a68554e1974da1bde0b6cceb6b413` |

From the Brine checkout, run:

```sh
BRINE_TEST_DILL=/tmp/brine-dill-test/dill \
BRINE_TEST_DILL_SCHEMA=/tmp/brine-dill-test/dill.pkl \
go test -count=1 -v ./internal/dill -run '^TestReleasedDill$'
```

The test checks the reported version, not the asset digest. It imports a temporary copy of the release schema locally and does not fetch Pkl packages, pull images, or contact a live socket.

## Coverage and limits

The suite uses the same runner methods as Brine for file validation, stdin validation, and formatting. It checks explicit socket selection using a nonexistent socket, rejection when no socket is configured, private runtime HOME, relative imports, invalid Pkl and schema types, and formatting without modifying the source file. For malformed Pkl, it also checks Dill's exit code `1` and JSON `evaluator_failure` separately from the diagnostic Brine returns. It does not test plan, lifecycle operations, or the web/API layers.
