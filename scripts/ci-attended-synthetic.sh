#!/usr/bin/env bash
# Disposable GHA Linux ONLY: real history + fake producer, NEVER qualification.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_OS:-} == Linux && $(id -u) != 0 ]]
work=$(mktemp -d "${RUNNER_TEMP:?}/brine-attended-synthetic.XXXXXXXX")
fixture_pid=''
cleanup() {
  result=$?
  if [[ -n $fixture_pid ]]; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  if [[ $result != 0 ]]; then
    python3 - "$work" <<'PY'
import pathlib, sys
for name in ('go-events.jsonl', 'fixture.log', 'browser.json'):
    p = pathlib.Path(sys.argv[1]) / name
    if p.is_file():
        print('RETAINED FAILURE', name)
        print(p.read_text(errors='replace'))
PY
  fi
  exit "$result"
}
trap cleanup EXIT
fixture_home=$(sudo mktemp -d /brine-attended-test-home.XXXXXXXX)
sudo chown "$(id -u):$(id -g)" "$fixture_home"
python3 scripts/check-alpha-fixture-home.py require "$fixture_home"
# Resolve Go caches before changing HOME for the test/fixture subprocesses only.
export GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)"
HOME="$fixture_home" go test -race -count=1 -json ./internal/api \
  -run '^(TestAttendedRealHistoryTerminalWriteFaultSuppressesMarker|TestAttendedRealHistoryPreviewApplyAndLookup|TestAuthenticatedLookupReturnsOriginalV2Record)$' \
  >"$work/go-events.jsonl"
python3 - "$work/go-events.jsonl" <<'PY'
import json, sys
expected = {
    'TestAttendedRealHistoryTerminalWriteFaultSuppressesMarker',
    'TestAttendedRealHistoryTerminalWriteFaultSuppressesMarker/preview',
    'TestAttendedRealHistoryTerminalWriteFaultSuppressesMarker/apply',
    'TestAttendedRealHistoryPreviewApplyAndLookup',
    'TestAuthenticatedLookupReturnsOriginalV2Record',
}
events = [json.loads(line) for line in open(sys.argv[1])]
assert not any(e['Action'] in ('skip', 'fail') for e in events), 'Failure/skip is not evidence'
assert {e['Test'] for e in events if e['Action'] == 'pass' and 'Test' in e} == expected
for name in sorted(expected):
    print('ACTUAL LINUX REAL-STORE PASS:', name)
PY
go build -o "$work/brine-fixture" ./cmd/brine-fixture
HOME="$fixture_home" "$work/brine-fixture" --synthetic-attended --port 18179 >"$work/fixture.log" 2>&1 &
fixture_pid=$!
ready=false
for _ in $(seq 1 100); do
  kill -0 "$fixture_pid"
  if curl -fsS -D "$work/headers" http://127.0.0.1:18179/login > /dev/null; then
    ready=true
    break
  fi
  sleep .1
done
[[ $ready == true ]]
grep -qi '^X-Brine-Fixture: synthetic-attended-no-qualification' "$work/headers"
BRINE_ATTENDED_FIXTURE_URL=http://127.0.0.1:18179 \
BRINE_FIXTURE_BASE_URL=http://127.0.0.1:18179 \
BRINE_VISUAL_BASE_URL=http://127.0.0.1:18179 BRINE_VISUAL_PASSWORD=unused \
  npx playwright test tests/attended-real-fixture.spec.js --workers=1 --reporter=json >"$work/browser.json"
python3 - "$work/browser.json" <<'PY'
import json, sys
report = json.load(open(sys.argv[1]))
assert report['stats']['expected'] == 1 and report['stats']['skipped'] == 0
assert report['stats']['unexpected'] == 0 and report['stats']['flaky'] == 0
specs = []
def walk(suite):
    specs.extend(suite.get('specs', []))
    for child in suite.get('suites', []): walk(child)
for suite in report['suites']: walk(suite)
assert len(specs) == 1 and specs[0]['title'] == 'synthetic producer and real Linux history: authenticated preview, confirm, manual typed lookup'
assert len(specs[0]['tests']) == 1 and specs[0]['tests'][0]['status'] == 'expected'
assert [r['status'] for r in specs[0]['tests'][0]['results']] == ['passed']
print('ACTUAL AUTHENTICATED SYNTHETIC API/STORE BROWSER PASS:', specs[0]['title'])
print('No native/source/provider/application qualification or registry authority implied')
PY
