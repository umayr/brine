#!/usr/bin/env bash
# Compatibility entry point: no Alpha pairing is performed. Verify denial instead.
set -euo pipefail
cd "$(dirname "$0")/.."
out=$(go test ./internal/api ./internal/dill ./internal/config -count=1 -v -run '^TestDisabled')
printf '%s\n' "$out"
for name in TestDisabledHandlersAndRoutedAPI TestDisabledDirectSecretAndAlphaOperations TestDisabledFeatureConfiguration; do
  grep -F -- "--- PASS: $name " <<<"$out" >/dev/null || { echo "missing required test: $name" >&2; exit 1; }
done
if grep -q -- '--- SKIP:' <<<"$out"; then echo 'disabled-feature check unexpectedly skipped' >&2; exit 1; fi
