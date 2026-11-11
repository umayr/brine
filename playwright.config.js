const { defineConfig } = require('@playwright/test');

const externalFixture = process.env.BRINE_FIXTURE_BASE_URL;
const managedAuth = !process.env.BRINE_VISUAL_BASE_URL && !process.env.BRINE_VISUAL_PASSWORD;
process.env.BRINE_FIXTURE_BASE_URL ||= 'http://127.0.0.1:18082';
if (managedAuth) {
  process.env.BRINE_VISUAL_BASE_URL = 'http://127.0.0.1:18083';
  process.env.BRINE_VISUAL_PASSWORD = 'fixture-only-not-a-secret';
}

module.exports = defineConfig({
  testDir: './tests',
  testMatch: '**/*.spec.js',
  webServer: [
    ...(!externalFixture ? [{ command: 'go run ./cmd/brine-fixture --port 18082', url: 'http://127.0.0.1:18082', reuseExistingServer: false, timeout: 120000 }] : []),
    ...(managedAuth ? [{ command: 'go run ./cmd/brine-fixture --auth --port 18083', url: 'http://127.0.0.1:18083/login', reuseExistingServer: false, timeout: 120000 }] : []),
  ],
  use: {
    browserName: 'chromium',
    ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { launchOptions: { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } }
      : {}),
  },
});
