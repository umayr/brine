#!/usr/bin/env bash
# Disposable GitHub Linux jobs only. Source the exact pinned Dill qualification
# script so its dedicated daemon and EXIT cleanup remain active during Brine tests.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && $(uname -s) == Linux ]] || { echo 'Disposable Linux CI only' >&2; exit 1; }
mode=${1:?docker or podman-rootless}
case "$mode" in docker|podman-rootless) ;; *) exit 1 ;; esac
readonly commit=bfe4940051275272c86f9f209e49f926d07d893a
brine=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
candidate=${BRINE_DILL_CANDIDATE_REPO:?absolute pinned candidate checkout required}
[[ $candidate = /* && $(git -C "$candidate" rev-parse HEAD) == "$commit" ]] || { echo 'Incorrect Dill candidate' >&2; exit 1; }
[[ -z $(git -C "$candidate" status --porcelain) ]] || { echo 'Candidate checkout is not clean' >&2; exit 1; }
(cd "$brine" && go test -c -o "$RUNNER_TEMP/brine-alpha.test" ./internal/dill)
# Dill's script builds its candidate and exact engine, validates SO_PEERPIDFD
# mappings, and executes its own qualification. Its EXIT trap owns the daemon.
cd "$candidate"
# shellcheck source=/dev/null
source "$candidate/scripts/ci-alpha-secrets.sh" "$mode"
[[ $(git -C "$candidate" rev-parse HEAD) == "$commit" && -z $(git -C "$candidate" status --porcelain) ]] || { echo 'Candidate changed during build' >&2; exit 1; }
binary="$candidate/bin/dill"
checksum=$(sha256sum "$binary" | awk '{print $1}')
printf 'Paired Dill commit: %s\nPaired Dill binary SHA-256: %s\n' "$commit" "$checksum"
# Prove that the namespace maps container 0 to this fixture owner's host IDs;
# inspect the connected peer under SO_PEERPIDFD, never infer from the CLI user.
run_operator python3 - "$socket" "$pid" <<'PY'
import os, select, socket, struct, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sys.argv[1])
pid, uid, gid = struct.unpack('3i', s.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
assert pid == int(sys.argv[2]), 'dedicated peer changed after Dill qualification'
pidfd = s.getsockopt(socket.SOL_SOCKET, 77)
poll = select.poll(); poll.register(pidfd, select.POLLIN)
assert not poll.poll(0) and (uid, gid) == (os.getuid(), os.getgid())
for kind, expected in [('uid', os.getuid()), ('gid', os.getgid())]:
    rows = [tuple(map(int, line.split())) for line in open(f'/proc/{pid}/{kind}_map')]
    mapped = [host for inside, host, count in rows if inside == 0 and count > 0]
    assert mapped == [expected], f'{kind} 0 does not map to fixture owner'
assert not poll.poll(0), 'Exact peer exited while checking fixture mapping'
os.close(pidfd)
PY
# Dill's qualification preloads this image on the exact dedicated daemon.
fixture="$base/brine-readonly"
run_operator mkdir -m 0700 "$fixture"
run_operator env PATH=/usr/bin:/bin python3 - "$fixture" "$candidate/dill.pkl" "$engine" "$socket" <<'PY'
import json, os, pathlib, sys
root, schema, engine, sock = sys.argv[1:]
root = pathlib.Path(root)
source = root / 'synthetic-source'
source.write_text('synthetic-ci-reference-only\n')
source.chmod(0o400)
config = f'''amends "file://{schema}"
config {{
  name = "brine-ci-readonly"
  engine = "{engine}"
  secrets {{ new {{ name = "db" }} }}
  services {{ ["app"] {{
    image = "docker.io/library/alpine:3.20"
    pullPolicy = "never"
    command = List("sleep", "600")
    secrets {{ new {{ name = "db"; target = "/run/secrets/db"; uid = 0; gid = 0; mode = 256 }} }}
  }} }}
}}
'''
(root / 'compose.pkl').write_text(config)
policy = {'version':'dill.secret-policy/1','targets':[{'id':'lab-a','engine':engine,'socket':sock,'stacks':[{'name':'brine-ci-readonly','sources':[{'id':'db-source','revision':'r1','kind':'hostFile','path':str(source),'uid':os.getuid(),'gid':os.getgid(),'mode':256}]}]}]}
binding = {'version':'dill.secret-bindings/1','target':'lab-a','stack':'brine-ci-readonly','bindings':[{'name':'db','source':'db-source','revision':'r1'}]}
for name, value in [('policy.json',policy),('binding.json',binding)]:
    (root / name).write_text(json.dumps(value))
for name in ['compose.pkl','policy.json','binding.json']:
    (root / name).chmod(0o600)
PY
home=$HOME
if [[ $operator == 0 ]]; then home=/root; fi
common=(PATH=/usr/bin:/usr/local/bin:/bin LANG=C LC_ALL=C HOME="$home"
 GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$candidate"
 BRINE_DILL_CANDIDATE="$binary" BRINE_DILL_CANDIDATE_COMMIT="$commit"
 BRINE_DILL_CANDIDATE_REPO="$candidate" BRINE_DILL_CANDIDATE_SHA256="$checksum"
 BRINE_DILL_PKL="$base/pkl" BRINE_DILL_SCHEMA="$candidate/dill.pkl"
 BRINE_DILL_ENGINE="$engine" BRINE_DILL_SOCKET="$socket" BRINE_DILL_TARGET=lab-a
 BRINE_DILL_STACK=brine-ci-readonly BRINE_DILL_CONFIG="$fixture/compose.pkl"
 BRINE_DILL_POLICY="$fixture/policy.json" BRINE_DILL_BINDINGS="$fixture/binding.json"
 BRINE_DILL_DISPOSABLE_MUTATION=I_HAVE_A_DISPOSABLE_LINUX_ENGINE
 BRINE_DILL_TEST_IMAGE=docker.io/library/alpine:3.20 BRINE_DILL_TEST_GRANT_UID=0 BRINE_DILL_TEST_GRANT_GID=0)
# A skipped selected test is a failed qualification, not a green job.
log="$RUNNER_TEMP/brine-paired-alpha.log"
run_operator env -i "${common[@]}" "$RUNNER_TEMP/brine-alpha.test" \
 -test.timeout=12m -test.v -test.run '^TestCandidateDillSecret(ReadOnlyIntegration|DisposableMutation)$' | tee "$log"
if grep -q -- '--- SKIP:' "$log"; then echo 'Paired test unexpectedly skipped' >&2; exit 1; fi
for test in TestCandidateDillSecretReadOnlyIntegration TestCandidateDillSecretDisposableMutation; do
 grep -F -- "--- PASS: $test " "$log" >/dev/null || { echo "Missing pass: $test" >&2; exit 1; }
done
