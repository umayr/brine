const { test, expect } = require('@playwright/test');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('mobile keyboard reveal clears on denied session; log endings are announced', async ({ page }) => {
  await page.setViewportSize({width: 320, height: 640});
  const fixture = path.join(os.tmpdir(), `brine-mobile-${process.pid}.html`);
  try {
    execFileSync('go', ['test', './internal/remoteui', '-run', '^TestBrowserFixture$', '-count=1'], {env:{...process.env, BRINE_REMOTE_FIXTURE:fixture}});
    const html = fs.readFileSync(fixture, 'utf8');
    let sessionChecks=0, logCalls=0;
    await page.route('http://remote.test/**', async route => {
      const url = route.request().url();
      if (url.endsWith('/remote/session')) {
        sessionChecks++;
        await route.fulfill(sessionChecks === 1 ? {contentType:'application/json',body:JSON.stringify({remainingMs:60000})} : {status:403,body:'denied'});
      } else if (url.endsWith('/remote/reveal')) {
        await route.fulfill({contentType:'application/json',body:JSON.stringify([{key:'TOKEN',value:'secret-value'}])});
      } else if (url.endsWith('/remote/logs')) {
        logCalls++;
        const end = logCalls === 1 ? {type:'end',reason:'byte_limit'} : {type:'error',reason:'incomplete'};
        await route.fulfill({contentType:'application/x-ndjson',body:JSON.stringify({type:'chunk',stream:'stdout',bytesBase64:'aGVsbG8='})+'\n'+JSON.stringify(end)+'\n'});
      } else await route.fulfill({contentType:'text/html',body:html});
    });
    await page.goto('http://remote.test/remote/inspect');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
    const confirm=page.getByLabel('Reveal environment values for this container now');
    await confirm.focus(); await page.keyboard.press('Space');
    await expect(confirm).toBeChecked();
    await page.keyboard.press('Tab'); await expect(page.getByRole('button',{name:'Reveal values'})).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('#values')).toHaveText('TOKEN=secret-value');
    await expect(page.locator('#values')).toBeEmpty({timeout:5000});
    await expect(page.getByRole('status')).toContainText('Session unavailable');
    await page.getByRole('button',{name:'Show last 100 log lines'}).click();
    await expect(page.locator('#log-output')).toHaveText('hello');
    await expect(page.getByRole('status')).toContainText('byte_limit');
    await page.getByRole('button',{name:'Show last 100 log lines'}).click();
    await expect(page.getByRole('status')).toContainText('incomplete');
  } finally { if (fs.existsSync(fixture)) fs.unlinkSync(fixture); }
});

test('short session expiry clears revealed values', async ({page}) => {
  const fixture = path.join(os.tmpdir(), `brine-expiry-${process.pid}.html`);
  try {
    execFileSync('go',['test','./internal/remoteui','-run','^TestBrowserFixture$','-count=1'],{env:{...process.env,BRINE_REMOTE_FIXTURE:fixture}});
    const html=fs.readFileSync(fixture,'utf8');
    await page.route('http://remote.test/**',async route=>{
      const url=route.request().url();
      if(url.endsWith('/remote/session')) await route.fulfill({contentType:'application/json',body:JSON.stringify({remainingMs:1800})});
      else if(url.endsWith('/remote/reveal')) await route.fulfill({contentType:'application/json',body:JSON.stringify([{key:'TOKEN',value:'secret-value'}])});
      else await route.fulfill({contentType:'text/html',body:html});
    });
    await page.goto('http://remote.test/remote/inspect');
    await page.getByLabel('Reveal environment values for this container now').check();
    await page.getByRole('button',{name:'Reveal values'}).click();
    await expect(page.locator('#values')).toHaveText('TOKEN=secret-value');
    await expect(page.locator('#values')).toBeEmpty({timeout:3000});
  } finally { if (fs.existsSync(fixture)) fs.unlinkSync(fixture); }
});
