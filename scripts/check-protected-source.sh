#!/usr/bin/env bash
# Run from the repository root only on a disposable GitHub-hosted runner.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $(uname -sm) == 'Linux x86_64' && $EUID == 0 ]] || {
  echo 'Requires root on disposable GitHub-hosted x86_64 Linux CI' >&2
  exit 1
}
[[ $# == 1 && $1 == /* && -x $1 ]] || {
  echo 'Expected an absolute path to the freshly built fixture helper' >&2
  exit 1
}
capture_helper=$1
# Fail closed when the application pins move; update the verified hashes together.
grep -qx 'ARG DILL_VERSION=0.12.0' Dockerfile
grep -qx 'ARG PKL_VERSION=0.31.1' Dockerfile
umask 022
root=$(mktemp -d /tmp/brine-protected-source.XXXXXXXX)
trap 'rm -rf -- "$root"' EXIT
cd "$root"
chmod 0755 "$root"
mkdir workspace export home
curl -fsSL --retry 3 https://github.com/umayr/dill/releases/download/v0.12.0/dill-linux-amd64 -o dill
curl -fsSL --retry 3 https://github.com/apple/pkl/releases/download/0.31.1/pkl-linux-amd64 -o pkl
sha256sum -c <<'SUMS'
27e4298ccb60609f4405fbcdf387617617c2e4dcd74119191f86062cf791c126  dill
618f13955d755cafbfe8c9cba1d27635848cd49dbc6abffd398d2751db1231bf  pkl
SUMS
chmod 0555 dill pkl
cat >workspace/compose.pkl <<'PKL'
import "values.pkl"

// Dill invokes plain `pkl eval` and expects JSON.
output {
  renderer = new JsonRenderer {}
}

config {
  name = values.stackName
  engine = "docker"
  services = new Mapping {
    ["web"] = new Dynamic { image = "alpine:3.22" }
  }
}
PKL
printf 'stackName = "captured-source"\n' >workspace/values.pkl
# The helper captures in memory, edits the workspace, and publishes only
# captured bytes. The manifest it emits is from the capture, not the export.
"$capture_helper" >manifest
chmod 0555 export

# Drop groups, capabilities and privilege escalation for both the controls and
# the provider. Use no inherited environment or engine credentials.
uid=$(id -u nobody)
gid=$(id -g nobody)
[[ $uid != 0 && $gid != 0 ]]
chown "$uid:$gid" home
as_reader=(setpriv --reuid="$uid" --regid="$gid" --clear-groups --bounding-set=-all --no-new-privs
  env -i PATH=/usr/bin:/bin HOME="$root/home" DILL_PKL_PATH="$root/pkl")
"${as_reader[@]}" sh -ec '
  test "$(id -u)" = "$1"
  test "$(id -g)" = "$3"
  test -w "$HOME"
  for path in / "$2" "$2/export" "$2/export/compose.pkl" "$2/export/values.pkl"; do
    test "$(stat -c %u "$path")" = 0 && test ! -w "$path" || exit 1
  done
  # Sticky /tmp prevents this reader from renaming the root-owned fixture.
  # The hosted runner and root provisioner are trusted; no other writer is
  # part of this fixture. These checks do not prove protection against root.
  test -k /tmp && test "$(stat -c %u /tmp)" = 0
  # Opening without truncation tests write access without changing the capture.
  python3 -I -c "import os, sys
try:
    fd = os.open(sys.argv[1], os.O_WRONLY)
except PermissionError:
    sys.exit(0)
os.close(fd)
sys.exit(1)" "$2/export/values.pkl"
' sh "$uid" "$root" "$gid"
verify_manifest() {
  (cd export && [[ $(find . -mindepth 1 | LC_ALL=C sort) == $'./compose.pkl\n./values.pkl' ]] && sha256sum -c ../manifest)
}
validate() {
  local path=$1 expected=$2 receipt
  receipt=$("${as_reader[@]}" "$root/dill" validate -f "$path" --format operation-v1)
  # Slurp so an extra JSON value cannot be hidden by a matching last receipt.
  jq -es --arg expected "$expected" '. == [{version:"dill.validate/1", operation:"validate", outcome:"succeeded", engineEffects:"none", stack:$expected}]' <<<"$receipt" >/dev/null
}
verify_manifest
validate "$root/workspace/compose.pkl" edited-workspace
validate "$root/export/compose.pkl" captured-source
verify_manifest
echo 'PASS: Dill validated the protected Brine capture, not the edited workspace'
