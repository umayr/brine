#!/usr/bin/env bash
# Exact-source compatibility only: no Docker CLI/real socket/native qualification.
# Run on a disposable Linux runner or isolated Linux no-socket test container.
set -euo pipefail
[[ $(uname -s) == Linux && ${BRINE_OPERATION_ISOLATED:-} == 1 ]] || {
  echo 'Requires explicitly isolated disposable Linux execution; no Mac candidate execution' >&2
  exit 2
}
readonly commit=b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2
readonly schema_sha=b34206d0622fc6c1aa99d5edefb9b6d4b01232635756d2c67fb135161f8db345
brine=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
candidate=${BRINE_OPERATION_CANDIDATE_REPO:?absolute exact-b3 source checkout required}
[[ $candidate = /* ]] || { echo 'Candidate path must be absolute' >&2; exit 1; }
check_source() {
  [[ $(git -C "$candidate" rev-parse HEAD) == "$commit" && -z $(git -C "$candidate" status --porcelain --untracked-files=all) ]] || {
    echo 'Dill source is not exact clean b3 (no release/source SHA substitution)' >&2; exit 1;
  }
  [[ $(sha256sum "$candidate/dill.pkl" | awk '{print $1}') == "$schema_sha" ]] || { echo 'Candidate schema hash mismatch' >&2; exit 1; }
  for name in manifest success failure unsupported-operation unsupported-version qualified-success qualified-cleanup-failure; do
    cmp "$candidate/internal/operation/testdata/$name.json" "$brine/internal/dill/testdata/operation-v1/$name.json"
  done
}
check_source
# Nothing is copied from real HOME: fresh evaluator, registry and build homes.
base=$(mktemp -d /tmp/brine-operation-contract.XXXXXXXX)
chmod 700 "$base"
# Retain artifacts on failure for the disposable runner's lifetime. No credentials.
mkdir -m 700 "$base/home" "$base/cache" "$base/modcache" "$base/tmp"
readonly go_bin=$(command -v go)
readonly build_path="$(dirname "$go_bin"):/usr/local/bin:/usr/bin:/bin"
clean_env=(PATH="$build_path" HOME="$base/home" LANG=C LC_ALL=C
  TMPDIR="$base/tmp" GOCACHE="$base/cache" GOMODCACHE="$base/modcache"
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$candidate")
case $(uname -m) in
  x86_64) asset=pkl-linux-amd64 ;;
  aarch64|arm64) asset=pkl-linux-aarch64 ;;
  *) echo 'Unsupported Linux Pkl architecture' >&2; exit 1 ;;
esac
pkl_sha=$(python3 - "$candidate/internal/loader/pkl-toolchain.json" "$asset" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p['version']=='0.31.1'
print(p['sha256'][sys.argv[2]])
PY
)
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  "https://github.com/apple/pkl/releases/download/0.31.1/$asset" -o "$base/pkl"
printf '%s  %s\n' "$pkl_sha" "$base/pkl" | sha256sum --check
chmod 700 "$base/pkl"
# Use Dill's actual Makefile/tags, not a direct go build or release substitution.
(cd "$candidate" && env -i "${clean_env[@]}" make build)
check_source
binary="$candidate/bin/dill"
env -i "${clean_env[@]}" "$go_bin" version -m "$binary" > "$base/build-info.txt"
grep -F "vcs.revision=$commit" "$base/build-info.txt" >/dev/null
grep -F 'vcs.modified=false' "$base/build-info.txt" >/dev/null
checksum=$(sha256sum "$binary" | awk '{print $1}')
printf 'Operation candidate commit: %s\nSchema SHA-256: %s\nBinary SHA-256: %s\n' "$commit" "$schema_sha" "$checksum"
# Compile/run the actual Brine package with candidate selected explicitly. The
# process suite binds only its private synthetic Unix listener (GET/HEAD only).
(cd "$brine" && env -i "${clean_env[@]}" \
  BRINE_OPERATION_CANDIDATE="$binary" BRINE_OPERATION_CANDIDATE_SHA256="$checksum" \
  BRINE_OPERATION_CANDIDATE_REPO="$candidate" BRINE_OPERATION_PKL="$base/pkl" \
  "$go_bin" test ./internal/dill -count=1 -v -timeout=5m \
  -run '^TestOperationContract' ) | tee "$base/results.log"
if grep -q -- '--- SKIP:' "$base/results.log"; then echo 'Selected contract test skipped; no positive evidence' >&2; exit 1; fi
for name in PinnedFixtureIntegrity ConsumerFixtures ConsumerCapabilities CandidateProcess; do
  grep -F -- "--- PASS: TestOperationContract$name " "$base/results.log" >/dev/null || {
    echo "Missing required contract pass: $name" >&2; exit 1;
  }
done
check_source
echo 'PASS: Brine consumer + exact b3 CLI read-only synthetic compatibility only; native engine/app/browser NOT RUN'
