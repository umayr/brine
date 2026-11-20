#!/usr/bin/env bash
# Run from the repository root only on a disposable GitHub-hosted runner.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && $(uname -sm) == 'Linux x86_64' && $EUID == 0 ]] || {
  echo 'Requires root on disposable GitHub-hosted x86_64 Linux CI' >&2
  exit 1
}
# Fail closed when the application pins move; update the verified hashes together.
grep -qx 'ARG DILL_VERSION=0.8.1' Dockerfile
grep -qx 'ARG PKL_VERSION=0.31.1' Dockerfile
umask 022
root=$(mktemp -d /tmp/brine-protected-source.XXXXXXXX)
trap 'rm -rf -- "$root"' EXIT
cd "$root"
chmod 0755 "$root"
mkdir workspace export home
curl -fsSL --retry 3 https://github.com/umayr/dill/releases/download/v0.8.1/dill-linux-amd64 -o dill
curl -fsSL --retry 3 https://github.com/apple/pkl/releases/download/0.31.1/pkl-linux-amd64 -o pkl
sha256sum -c <<'SUMS'
e27c4e6597f5b8fd12518a82d2f3e59d87ca59625f43bbdda6cc47fa85943cc0  dill
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
(cd workspace && sha256sum compose.pkl values.pkl) >manifest
install -m 0444 workspace/{compose,values}.pkl export/
chmod 0555 export
printf 'stackName = "edited-workspace"\n' >workspace/values.pkl

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
  test -w "$HOME" && test -r "$2/export/values.pkl"
  for file in / /tmp "$2" "$2/export" "$2/export/compose.pkl" "$2/export/values.pkl"; do
    if test -w "$file" && test "$file" != /tmp; then exit 1; fi
  done
  # Sticky /tmp prevents this reader from renaming the root-owned fixture.
  test -k /tmp && test "$(stat -c %u /tmp)" = 0
  # Opening without truncation tests write access without changing the capture.
  python3 -I -c "import os, sys
try:
    fd = os.open(sys.argv[1], os.O_WRONLY)
except PermissionError:
    sys.exit(0)
os.close(fd)
sys.exit(1)" "$2/export/values.pkl"
' sh "$uid" "$root"
verify_manifest() {
  (cd export && [[ $(find . -mindepth 1 | LC_ALL=C sort) == $'./compose.pkl\n./values.pkl' ]] && sha256sum -c ../manifest)
}
validate() {
  local path=$1 expected=$2 receipt
  receipt=$("${as_reader[@]}" "$root/dill" validate -f "$path" --format operation-v1)
  jq -e --arg expected "$expected" '. == {version:"dill.validate/1", operation:"validate", outcome:"succeeded", engineEffects:"none", stack:$expected}' <<<"$receipt" >/dev/null
}
verify_manifest
validate "$root/workspace/compose.pkl" edited-workspace
validate "$root/export/compose.pkl" captured-source
verify_manifest
echo 'PASS: Dill validated the protected capture, not the edited workspace'
