#!/usr/bin/env bash
# Compatibility entry point: attended Alpha is disabled, not qualified.
set -euo pipefail
cd "$(dirname "$0")/.."
out=$(go test ./internal/api -count=1 -v -run '^TestDisabledHandlersAndRoutedAPI$')
printf '%s\n' "$out"
grep -F -- '--- PASS: TestDisabledHandlersAndRoutedAPI ' <<<"$out" >/dev/null
if grep -q -- '--- SKIP:' <<<"$out"; then echo 'disabled-handler check unexpectedly skipped' >&2; exit 1; fi
