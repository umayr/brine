const {test, expect} = require('@playwright/test');
const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('duplicate inventory names expose distinct IDs accessibly', async ({page}) => {
  const fixture=path.join(os.tmpdir(),`brine-inventory-${process.pid}.html`);
  try {
    execFileSync('go',['test','./internal/remoteui','-run','^TestBrowserFixture$','-count=1'],{env:{...process.env,BRINE_REMOTE_FIXTURE:fixture,BRINE_REMOTE_INVENTORY:'1'}});
    await page.setContent(fs.readFileSync(fixture,'utf8'));
    const rows=page.locator('li');await expect(rows).toHaveCount(2);
    for (const [index,letter] of ['a','b'].entries()) {
      await expect(rows.nth(index).locator('code')).toHaveText(letter.repeat(12));
      await expect(rows.nth(index).locator('.sr-only')).toHaveText('Full container ID: '+letter.repeat(64));
      await expect(rows.nth(index).getByRole('link',{name:'Inspect same, full container ID '+letter.repeat(64)})).toHaveAttribute('href',new RegExp('container='+letter.repeat(64)));
    }
  } finally {if(fs.existsSync(fixture))fs.unlinkSync(fixture)}
});

test('unavailable inventory can be refreshed over GET without JavaScript', async ({ browser }) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'brine-inventory-refresh-'));
  const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 320, height: 640 } });
  try {
    const fixtures = {};
    for (const state of ['unavailable', 'empty']) {
      const fixture = path.join(dir, `${state}.html`);
      execFileSync('go', ['test', './internal/remoteui', '-run', '^TestBrowserFixture$', '-count=1'], {
        env: { ...process.env, BRINE_REMOTE_FIXTURE: fixture, BRINE_REMOTE_INVENTORY: state },
      });
      fixtures[state] = fixture;
    }
    const url = 'http://remote.test/remote/inventory?host=h';
    const requests = [];
    await context.route('http://remote.test/**', async (route) => {
      const request = route.request();
      requests.push([request.method(), request.url()]);
      const unavailable = requests.length === 1;
      await route.fulfill({
        contentType: 'text/html', status: unavailable ? 503 : 200,
        path: unavailable ? fixtures.unavailable : fixtures.empty,
      });
    });
    const page = await context.newPage();
    const response = await page.goto(url);
    expect(response.status()).toBe(503);
    await expect(page.getByRole('heading', { name: 'Dill-managed containers on h' })).toBeVisible();
    await expect(page.getByRole('alert')).toContainText('The container list is unknown');
    const empty = page.getByText('Verified inventory returned no Dill-managed containers.');
    await expect(empty).toHaveCount(0);
    await expect(page.getByRole('link', { name: /^Inspect/ })).toHaveCount(0);
    const refresh = page.getByRole('link', { name: 'Refresh inventory' });
    await refresh.focus();
    await expect(refresh).toBeFocused();
    await refresh.press('Enter');
    await expect(empty).toBeVisible();
    await expect(page.getByRole('alert')).toHaveCount(0);
    expect(requests).toEqual([['GET', url], ['GET', url]]);
  } finally {
    await context.close();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
