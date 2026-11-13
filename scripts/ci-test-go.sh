#!/usr/bin/env bash
# Disposable Linux CI runner only. Never copy credentials or repair host policy.
set -euo pipefail

# Resolve these BEFORE changing HOME; retain setup-go's module/build caches.
GOPATH=$(go env GOPATH)
GOCACHE=$(go env GOCACHE)
test -n "$GOPATH" && test -n "$GOCACHE"
export GOPATH GOCACHE
python3 scripts/test-check-alpha-fixture-home.py
printf '%s\n' 'Original HOME ancestry (metadata only; historical /home ACL is not assumed):'
python3 scripts/check-alpha-fixture-home.py report "$HOME"

# /tmp is intentionally rejected, and /home may carry inherited/default ACLs.
# Only a fresh unique directory directly under / is provisioned with sudo.
# mktemp creates0700 atomically. No chmod/setfacl of /, /home or existing files.
fixture_home=$(sudo mktemp -d /brine-go-test-home.XXXXXXXX)
sudo chown "$(id -u):$(id -g)" "$fixture_home"
printf '%s\n' 'Synthetic HOME ancestry (required safe; no credential contents):'
python3 scripts/check-alpha-fixture-home.py require "$fixture_home"
export HOME="$fixture_home"

# Retain this synthetic directory until the disposable runner is destroyed.
# Verbose ACL records make setup failures/skips distinguishable from execution.
go test -race -count=1 -v ./internal/auth -run '^TestCredentialLinux'
go test -race ./...
