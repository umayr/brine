#!/usr/bin/env bash
# Test-only, disposable GitHub-hosted Linux guest. Do not run on an operator host.
set -euo pipefail

if [[ ${1:-} == namespace ]]; then
  shift
  [[ $# == 6 && $EUID == 0 ]]
  root=$1 binary=$2 old_net=$3 old_mnt=$4 sha=$5 profile=$6
  case "$profile" in
    lifecycle) tests='^TestRestartOnlyHistoryLifecycle$' ;;
    storage) tests='^TestObservationStore(PreservesPresenceAndEnrollment|RejectsPrototypeWithoutAdopting)$' ;;
    *) exit 2 ;;
  esac
  [[ $root =~ ^/brine-214-verify\.[A-Za-z0-9]{8}$ && -d $root && ! -L $root ]]
  [[ $(readlink /proc/self/ns/net) != "$old_net" && $(readlink /proc/self/ns/mnt) != "$old_mnt" ]]
  [[ $(ip -o link show | awk -F ': ' '{print $2}') == lo && -z $(ip -4 -o route show table all) && -z $(ip -6 -o route show table all) ]]
  [[ ! $(ip -o link show lo) =~ \<([A-Z_,]*,)?UP([,\>]) ]]
  mount -t tmpfs -o size=16m,nr_inodes=128,mode=0755,nosuid,nodev tmpfs "$root"
  [[ $(findmnt -n -o FSTYPE --target "$root") == tmpfs ]]
  install -d -m 0700 -o 65532 -g 65532 "$root/fixtures"
  install -m 0555 -- "$binary" "$root/history.test"
  [[ $(sha256sum -- "$root/history.test" | awk '{print $1}') == "$sha" ]]
  ulimit -u 64
  ulimit -c 0
  exec python3 - "$root" "$tests" <<'PY'
import os
import resource
import shutil
import sys

chroot = shutil.which("chroot", path="/usr/sbin:/usr/bin:/sbin:/bin")
setpriv = shutil.which("setpriv", path="/usr/sbin:/usr/bin:/sbin:/bin")
assert chroot and setpriv
maxfd = resource.getrlimit(resource.RLIMIT_NOFILE)[0]
assert 3 <= maxfd <= 1048576
os.closerange(3, maxfd)
os.execve(setpriv, [setpriv, "--no-new-privs", chroot, "--userspec=65532:65532", "--groups=65532",
                   sys.argv[1], "/history.test", "-test.run", sys.argv[2], "-test.v", "-test.count=1", "-test.timeout=20s"],
          {"HOME": "/fixtures", "TMPDIR": "/fixtures", "LANG": "C", "TZ": "UTC", "GOMAXPROCS": "2"})
PY
fi

[[ $# == 3 && ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && ${RUNNER_OS:-} == Linux && ${RUNNER_ARCH:-} == X64 ]]
[[ $EUID != 0 && $(uname -sm) == 'Linux x86_64' ]]
binary=$(realpath -- "$1")
sha=$2
profile=$3
[[ $profile == lifecycle || $profile == storage ]]
[[ $sha =~ ^[0-9a-f]{64}$ && -O $binary && -f $binary && ! -L $1 ]]
[[ $(sha256sum -- "$binary" | awk '{print $1}') == "$sha" ]]
root=$(sudo -n mktemp -d /brine-214-verify.XXXXXXXX)
[[ $root =~ ^/brine-214-verify\.[A-Za-z0-9]{8}$ && ! -L $root ]]
log=$(mktemp "$RUNNER_TEMP/brine-214-log.XXXXXXXX")
set +e
sudo -n env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C \
  timeout --kill-after=5s 30s unshare --mount --propagation private --net --pid --fork --kill-child \
  bash --noprofile --norc "$(realpath -- "$0")" namespace "$root" "$binary" \
  "$(readlink /proc/self/ns/net)" "$(readlink /proc/self/ns/mnt)" "$sha" "$profile" </dev/null 2>&1 | head -c 16385 >"$log"
statuses=("${PIPESTATUS[@]}")
set -e
[[ ${statuses[1]} == 0 ]]
python3 - "${statuses[0]}" "$log" "$profile" <<'PY'
import pathlib
import re
import sys

status, log, profile = sys.argv[1:]
expected = {"lifecycle": {"TestRestartOnlyHistoryLifecycle"},
            "storage": {"TestObservationStorePreservesPresenceAndEnrollment", "TestObservationStoreRejectsPrototypeWithoutAdopting"}}[profile]
p = pathlib.Path(log)
if p.stat().st_size > 16384:
    sys.exit("qualification log limit exceeded; no raw output")
report = p.read_text(errors="replace")
seen = {name: [] for name in expected}
for outcome, name in re.findall(r"^--- (PASS|SKIP|FAIL): ([A-Za-z0-9_]+) \(", report, re.M):
    if name in seen:
        seen[name].append(outcome)
print(profile, "isolated process exit:", status)
for name, results in sorted(seen.items()):
    print(name, "/".join(results) or "MISSING")
if status != "0" or any(results != ["PASS"] for results in seen.values()):
    sys.exit("qualification failed or incomplete; raw output not published")
PY
# The namespace releases the private mount and children. Guest teardown clears
# the empty mountpoint, build outputs, and private log: no privileged deletion.
