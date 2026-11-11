const test = require('node:test');
const assert = require('node:assert/strict');

test('managed fixture servers receive bounded graceful shutdown', () => {
  delete process.env.BRINE_FIXTURE_BASE_URL;
  delete process.env.BRINE_VISUAL_BASE_URL;
  delete process.env.BRINE_VISUAL_PASSWORD;
  const config = require('../playwright.config.js');
  assert.equal(config.webServer.length, 2);
  for (const server of config.webServer) {
    assert.deepEqual(server.gracefulShutdown, { signal: 'SIGTERM', timeout: 5000 });
  }
});
