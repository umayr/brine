const { defineConfig } = require('@playwright/test');

const externalFixture = process.env.BRINE_FIXTURE_BASE_URL;
const managedAuth = !process.env.BRINE_VISUAL_BASE_URL && !process.env.BRINE_VISUAL_PASSWORD;
process.env.BRINE_FIXTURE_BASE_URL ||= 'http://127.0.0.1:18082';
if (managedAuth) {
  process.env.BRINE_VISUAL_BASE_URL = 'http://127.0.0.1:18083';
  process.env.BRINE_VISUAL_PASSWORD = 'fixture-only-not-a-secret';
  process.env.BRINE_STAGING_BASE_URL = 'http://127.0.0.1:18084';
  process.env.BRINE_WEBHOOK_FIXTURE_BASE_URL = 'http://127.0.0.1:18086';
}

module.exports = defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.js',
  webServer: [
    ...(process.env.BRINE_CREDENTIAL_FIXTURE_DIR ? [{ command: 'bash scripts/credential-browser-fixture.sh', url: 'http://127.0.0.1:18085/login', reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 }, timeout: 120000 }] : []),
    ...(!externalFixture ? [{ command: 'go run ./cmd/brine-fixture --port 18082', url: 'http://127.0.0.1:18082', reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 }, timeout: 120000 }] : []),
    ...(managedAuth ? [{ command: 'go run ./cmd/brine-fixture --synthetic-webhooks --port 18086', url: 'http://127.0.0.1:18086/login', reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 }, timeout: 120000 }, { command: 'go run ./cmd/brine-fixture --auth --port 18083', url: 'http://127.0.0.1:18083/login', reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 }, timeout: 120000 }, { command: 'go run ./cmd/brine-fixture --synthetic-staging --port 18084', url: 'http://127.0.0.1:18084/login', reuseExistingServer: false, gracefulShutdown: { signal: 'SIGTERM', timeout: 5000 }, timeout: 120000 }] : []),
  ],
  use: {
    browserName: 'chromium',
    ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } }
      : {}),
  },
});
