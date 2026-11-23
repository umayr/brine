#!/usr/bin/env bash
# Test-only GitHub-hosted disposable guest. Never run this on an operator host.
set -euo pipefail

if [[ ${1:-} == namespace ]]; then
  shift
  [[ $# == 5 && $EUID == 0 ]]
  root=$1 binary=$2 old_net=$3 old_mnt=$4 sha=$5
  [[ $root =~ ^/brine-213-verify\.[A-Za-z0-9]{8}$ && -d $root && ! -L $root ]]
  [[ $(readlink /proc/self/ns/net) != "$old_net" && $(readlink /proc/self/ns/mnt) != "$old_mnt" ]]
  [[ $(ip -o link show | awk -F ': ' '{print $2}') == lo && -z $(ip -4 -o route show table all) && -z $(ip -6 -o route show table all) ]]
  [[ ! $(ip -o link show lo) =~ \<([A-Z_,]*,)?UP([,\>]) ]]
  mount -t tmpfs -o size=16m,nr_inodes=128,mode=0755,nosuid,nodev tmpfs "$root"
  [[ $(findmnt -n -o FSTYPE --target "$root") == tmpfs ]]
  install -d -m 0700 -o 65532 -g 65532 "$root/fixtures"
  install -m 0555 -- "$binary" "$root/sqlitestore.test"
  [[ $(sha256sum -- "$root/sqlitestore.test" | awk '{print $1}') == "$sha" ]]
  ulimit -u 64
  ulimit -c 0
  # Close inherited descriptors before chroot, then drop uid/gid and suppress new privileges.
  exec python3 - "$root" <<'PY'
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
pattern = ("^(TestHistoryDirectoryAdmission|TestDirectoryOwnershipFacts|"
           "TestPersistent(LockRestart|RejectPreserves|RecheckReplacement|ValidSidecarsPreserved|AbruptExit|RootReplacement)|"
           "TestPageLimitPreservesPriorRows|TestQualification(IndependentProcessOwner|ExitBeforeCommit))$")
os.execve(setpriv, [setpriv, "--no-new-privs", chroot, "--userspec=65532:65532", "--groups=65532",
                   sys.argv[1], "/sqlitestore.test", "-test.run", pattern, "-test.v", "-test.count=1", "-test.timeout=30s"],
          {"HOME": "/fixtures", "TMPDIR": "/fixtures", "LANG": "C", "TZ": "UTC", "GOMAXPROCS": "2"})
PY
fi

[[ $# == 2 && ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && ${RUNNER_OS:-} == Linux && ${RUNNER_ARCH:-} == X64 ]]
[[ $EUID != 0 && $(uname -sm) == 'Linux x86_64' ]]
binary=$(realpath -- "$1")
sha=$2
[[ $sha =~ ^[0-9a-f]{64}$ && -O $binary && -f $binary && ! -L $1 ]]
[[ $(sha256sum -- "$binary" | awk '{print $1}') == "$sha" ]]
# Exactly these newly allocated directories; root is never mounted in the host namespace.
root=$(sudo -n mktemp -d /brine-213-verify.XXXXXXXX)
[[ $root =~ ^/brine-213-verify\.[A-Za-z0-9]{8}$ && ! -L $root ]]
log=$(mktemp "$RUNNER_TEMP/brine-213-log.XXXXXXXX")
set +e
sudo -n env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C \
  timeout --kill-after=5s 45s unshare --mount --propagation private --net --pid --fork --kill-child \
  bash --noprofile --norc "$(realpath -- "$0")" namespace "$root" "$binary" \
  "$(readlink /proc/self/ns/net)" "$(readlink /proc/self/ns/mnt)" "$sha" </dev/null 2>&1 | head -c 16385 >"$log"
statuses=("${PIPESTATUS[@]}")
set -e
[[ ${statuses[1]} == 0 ]]
status=${statuses[0]}
# No raw process output, workspace paths, test bodies, service metadata or environment dump.
python3 - "$status" "$log" <<'PY'
import pathlib
import re
import sys

expected = {
    "TestHistoryDirectoryAdmission", "TestDirectoryOwnershipFacts", "TestPersistentLockRestart",
    "TestPersistentRejectPreserves", "TestPersistentRecheckReplacement", "TestPersistentValidSidecarsPreserved",
    "TestPersistentAbruptExit", "TestPersistentRootReplacement", "TestPageLimitPreservesPriorRows",
    "TestQualificationIndependentProcessOwner", "TestQualificationExitBeforeCommit",
}
p = pathlib.Path(sys.argv[2]); size = p.stat().st_size
if size > 16384:
    sys.exit("qualification log limit exceeded; no raw output")
report = p.read_text(errors="replace")
seen = {name: [] for name in expected}
for outcome, name in re.findall(r"^--- (PASS|SKIP|FAIL): ([A-Za-z0-9_]+) \(", report, re.M):
    if name in seen:
        seen[name].append(outcome)
print("isolated process exit:", sys.argv[1])
for name, results in sorted(seen.items()):
    print(name, "/".join(results) or "MISSING")
if sys.argv[1] != "0" or any(results != ["PASS"] for results in seen.values()):
    sys.exit("qualification failed or incomplete; raw output not published")
PY
# Namespace/tmpfs lifetimes end with the supervised process tree. Runner teardown
# disposes the empty synthetic mountpoint, build workspace and private log.
