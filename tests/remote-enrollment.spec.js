const { test, expect } = require('@playwright/test');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

test('enrollment review displays complete old and new records and requires confirmation', async ({ page }) => {
  const fixture = path.join(os.tmpdir(), `brine-enrollment-${process.pid}.html`);
  try {
    execFileSync('go', ['test', './internal/remoteui', '-run', '^TestEnrollmentBrowserFixture$', '-count=1'], { env: { ...process.env, BRINE_ENROLLMENT_FIXTURE: fixture } });
    const html = fs.readFileSync(fixture, 'utf8');
    await page.route('http://remote.test/**', route => route.fulfill({ contentType: 'text/html', body: html }));
    await page.goto('http://remote.test/remote/enrollment/review');
    await expect(page.getByText('Independently obtain and verify every pin and identity out of band.', { exact: false })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Previous record' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Complete candidate' })).toBeVisible();
    await expect(page.getByText('"host.example"', { exact: true })).toBeVisible();
    await expect(page.getByText('"updated.example"', { exact: true })).toBeVisible();
    const oldDaemon = page.locator('dl').nth(0).locator('dt').filter({ hasText: /^daemon_id$/ }).locator('xpath=following-sibling::dd[1]/code');
    const newDaemon = page.locator('dl').nth(1).locator('dt').filter({ hasText: /^daemon_id$/ }).locator('xpath=following-sibling::dd[1]/code');
    await expect(oldDaemon).toHaveText('"daemon\\x20x"');
    await expect(newDaemon).toHaveText('"daemon\\x20\\x20x"');
    await expect(newDaemon).toHaveCSS('white-space', 'pre-wrap');
    for (const field of ['id', 'revision', 'host', 'port', 'user', 'key_path', 'host_public_key', 'host_key_sha256', 'agent_installation_id', 'agent_public_key', 'agent_fingerprint', 'engine_id', 'engine_kind', 'engine_socket', 'remote_socket', 'daemon_id', 'generation', 'reveal_key_fingerprint']) {
      await expect(page.locator('dt', { hasText: new RegExp(`^${field}$`) })).toHaveCount(2);
    }
    const checkbox = page.getByRole('checkbox');
    await expect(checkbox).not.toBeChecked();
    await expect(checkbox).toHaveAttribute('required', '');
    await expect(page.locator('input[name="token"]')).toHaveValue('fixture-token');
    await expect(page.locator('input[name="host"]')).toHaveCount(0);
  } finally { fs.rmSync(fixture, { force: true }); }
});
