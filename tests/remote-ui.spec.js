const { test, expect } = require('@playwright/test');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('remote inspect reveals only after confirmation and clears values', async ({ page }) => {
  const fixture = path.join(os.tmpdir(), `brine-remote-${process.pid}.html`);
  try {
    execFileSync('go', ['test', './internal/remoteui', '-run', '^TestBrowserFixture$', '-count=1'], { env: { ...process.env, BRINE_REMOTE_FIXTURE: fixture } });
    const html = fs.readFileSync(fixture, 'utf8');
    let reveals = 0;
    await page.route('http://remote.test/**', async route => {
      if (route.request().url().endsWith('/remote/session')) {
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify({remainingMs: 60000}) });
      } else if (route.request().url().endsWith('/remote/reveal')) {
        reveals++;
        await route.fulfill({ contentType: 'application/json', body: JSON.stringify([{key:'TOKEN',value:'secret-value'}]) });
      } else await route.fulfill({ contentType: 'text/html', body: html });
    });
    await page.goto('http://remote.test/remote/inspect');
    await page.getByText('Command and environment names').click();
    await expect(page.getByText('Environment names only')).toBeVisible();
    await expect(page.locator('#values')).toBeEmpty();
    await page.getByRole('button', { name: 'Reveal values' }).click();
    expect(reveals).toBe(0);
    await page.getByLabel('Reveal environment values for this container now').check();
    await page.getByRole('button', { name: 'Reveal values' }).click();
    await expect(page.locator('#values')).toHaveText('TOKEN=secret-value');
    await page.getByRole('button', { name: 'Clear values' }).click();
    await expect(page.locator('#values')).toBeEmpty();
    expect(reveals).toBe(1);
  } finally { if (fs.existsSync(fixture)) fs.unlinkSync(fixture); }
});
