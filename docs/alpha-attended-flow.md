# Synthetic attended browser fixture (not qualification)

`brine-fixture --synthetic-attended` is an explicitly selected **Linux-only, disposable test fixture**, not a Brine application configuration or production activation. Its producer is fixed synthetic data: it never runs Dill or Pkl, opens an engine/socket, captures source, or proves native/runtime/source qualification. It uses the real Linux operation history store in a private temporary directory under the test account's home. The fixture allows only authenticated synthetic `observability/up` preview and confirmation; normal fixture mode is unchanged. The fixture returns `X-Brine-Fixture: synthetic-attended-no-qualification`.

Run the following only on an isolated non-root Linux GHA worker with the repository's pinned Playwright dependencies and browser installed. This test is not a replacement for native qualification. Choose an unused private loopback port; do not target another fixture or production server:

```sh
go build -o "$RUNNER_TEMP/brine-attended-fixture" ./cmd/brine-fixture
"$RUNNER_TEMP/brine-attended-fixture" --synthetic-attended --port 18179 >"$RUNNER_TEMP/brine-attended-fixture.log" 2>&1 &
fixture_pid=$!
trap 'kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true' EXIT
for i in $(seq 1 100); do
  if curl -fsS http://127.0.0.1:18179/login >/dev/null; then break; fi
  sleep .1
done
BRINE_ATTENDED_FIXTURE_URL=http://127.0.0.1:18179 \
BRINE_FIXTURE_BASE_URL=http://127.0.0.1:18179 \
BRINE_VISUAL_BASE_URL=http://127.0.0.1:18179 BRINE_VISUAL_PASSWORD=unused \
npx playwright test tests/attended-real-fixture.spec.js --workers=1
```

The browser logs in with the fixture-only credential, requests preview, explicitly confirms once, and manually looks up the linked V2 apply record. It asserts no automatic apply or polling. A synthetic `unknown` outcome is deliberately not a success or qualification claim. The fixture must never be connected to a real engine or provided source. Exact-head GHA and independent security review remain required; source factory, application startup wiring, and qualification gates remain separate and off.
