const { test, expect } = require('@playwright/test');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('mobile inspection discloses details safely with keyboard', async ({page}) => {
  const fixture = path.join(os.tmpdir(), `brine-inspection-${process.pid}.html`);
  try {
    execFileSync('go',['test','./internal/remoteui','-run','^TestBrowserFixture$','-count=1'],{env:{...process.env,BRINE_REMOTE_FIXTURE:fixture}});
    const html=fs.readFileSync(fixture,'utf8');
    await page.setViewportSize({width:320,height:640});
    await page.route('http://remote.test/**', route=>route.fulfill({contentType:'text/html',body:html}));
    await page.goto('http://remote.test/remote/inspect');
    await expect(page.getByText('image:tag')).toBeVisible();
    await expect(page.locator('#values')).toBeEmpty();
    await expect(page.locator('img')).toHaveCount(0);
    for (const title of ['Command and environment names','Ports, mounts and networks','Restart and health','Resources and security','Ownership labels']) {
      const summary=page.getByText(title,{exact:true});
      await summary.focus(); await page.keyboard.press('Enter');
      await expect(summary.locator('..')).toHaveAttribute('open','');
    }
    await expect(page.getByText('TOKEN',{exact:true})).toBeVisible();
    await expect(page.getByText('8080', {exact:false}).first()).toBeVisible();
    await expect(page.getByText('hostile <img src=x onerror=alert(1)>').first()).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  } finally { if(fs.existsSync(fixture)) fs.unlinkSync(fixture); }
});

test('keyboard navigation returns a direct inspection link to the same target inventory', async ({ browser }) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'brine-inspection-back-'));
  const context = await browser.newContext({ javaScriptEnabled: false });
  try {
    const origin = 'http://remote.test';
    const inspectURL = `${origin}/remote/inspect?host=h&container=${'a'.repeat(64)}`;
    const inventoryURL = `${origin}/remote/inventory?host=h`;
    const fixtures = new Map();
    for (const [url, inventory] of [[inspectURL, ''], [inventoryURL, '1']]) {
      const fixture = path.join(dir, inventory ? 'inventory.html' : 'inspect.html');
      execFileSync('go', ['test', './internal/remoteui', '-run', '^TestBrowserFixture$', '-count=1'], {
        env: { ...process.env, BRINE_REMOTE_FIXTURE: fixture, BRINE_REMOTE_INVENTORY: inventory },
      });
      fixtures.set(url, fixture);
    }
    const requests = [];
    await context.route(`${origin}/**`, (route) => {
      const request = route.request();
      requests.push([request.method(), request.url()]);
      const fixture = fixtures.get(request.url());
      return fixture ? route.fulfill({ contentType: 'text/html', path: fixture }) : route.abort();
    });
    const page = await context.newPage();
    await page.goto(inspectURL);
    const back = page.getByRole('link', { name: 'Back to inventory' });
    await expect(back).toBeVisible();
    await page.keyboard.press('Tab');
    await expect(back).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(inventoryURL);
    await expect(page.getByRole('heading', { name: 'Dill-managed containers on h' })).toBeVisible();
    expect(requests).toEqual([['GET', inspectURL], ['GET', inventoryURL]]);
  } finally {
    await context.close();
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
