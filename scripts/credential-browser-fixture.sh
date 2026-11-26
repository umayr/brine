#!/usr/bin/env bash
# Run the production server against unique synthetic credentials and no engine.
set -euo pipefail
umask 077
root=${BRINE_CREDENTIAL_FIXTURE_DIR:?Provide a private, canonical fixture directory}
state=$(mktemp -d "$root/general.XXXXXXXX")
unset BRINE_AUTH_USER BRINE_AUTH_PASSWORD BRINE_API_TOKEN
printf '%s\n' '{"user":"admin","password":"fixture-only-not-a-secret","api_token":"fixture-only-token"}' > "$state/credentials.json"
exec go run ./cmd/brine --bind 127.0.0.1 --port 18085 --data-dir "$state" --engine-socket "unix://$state/no-engine.sock" --dill "$state/no-dill"
