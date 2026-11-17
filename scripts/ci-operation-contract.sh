#!/usr/bin/env bash
# Compatibility entry point: qualified operations are disabled in Brine.
set -euo pipefail
cd "$(dirname "$0")/.."
out=$(go test ./internal/dill -count=1 -v -run '^TestDisabled(DirectSecretAndAlphaOperations|OperationDoesNotStartObserverOrWrite)$')
printf '%s\n' "$out"
for name in TestDisabledDirectSecretAndAlphaOperations TestDisabledOperationDoesNotStartObserverOrWrite; do
  grep -F -- "--- PASS: $name " <<<"$out" >/dev/null || { echo "missing required test: $name" >&2; exit 1; }
done
if grep -q -- '--- SKIP:' <<<"$out"; then echo 'disabled-operation check unexpectedly skipped' >&2; exit 1; fi
