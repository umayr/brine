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
