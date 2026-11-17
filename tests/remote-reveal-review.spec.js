const { test, expect } = require('@playwright/test');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

test('actual review CSP and cancellation prevent delayed values reappearing', async ({ page }) => {
  const fixture = path.join(os.tmpdir(), `brine-reveal-review-${process.pid}.json`);
  execFileSync('go', ['test', './internal/remoteui', '-run', '^TestRevealBrowserFixture$', '-count=1'], { env: { ...process.env, BRINE_REVEAL_FIXTURE: fixture } });
  const data = JSON.parse(fs.readFileSync(fixture, 'utf8'));
  expect(data.html).not.toContain('SYNTHETIC_SECRET');
  expect(data.csp).toContain("frame-ancestors 'none'");
  expect(data.csp).not.toContain('unsafe-inline');
  expect(data.frame).toBe('DENY');
  await page.addInitScript(() => {
    const nativeFetch=window.fetch; const pending=[];
    window.fetch=(...args)=>{const p=nativeFetch(...args); pending.push({url:String(args[0]),done:p.then(()=>{},()=>{})}); return p};
    window.settleFetch=async suffix=>{await Promise.all(pending.filter(x=>x.url.endsWith(suffix)).map(x=>x.done)); await new Promise(resolve=>setTimeout(resolve,0))};
    window.watchSecrets=()=>{window.secretAfterClear=false; new MutationObserver(records=>{for(const r of records){if((r.target.textContent||'').includes('SYNTHETIC_SECRET'))window.secretAfterClear=true; for(const n of r.addedNodes)if((n.textContent||'').includes('SYNTHETIC_SECRET'))window.secretAfterClear=true}}).observe(document.querySelector('#values'),{childList:true,subtree:true,characterData:true})};
  });
  let releaseApproval, approvalArrived, approvalFinished;
  const approvalDone = new Promise(resolve => approvalFinished = resolve);
  const approved = new Promise(resolve => approvalArrived = resolve);
  const release = new Promise(resolve => releaseApproval = resolve);
  await page.route('http://remote.test/remote/reveal/review', route => route.fulfill({ contentType: 'text/html', headers: { 'content-security-policy': data.csp, 'x-frame-options': data.frame }, body: data.html }));
  await page.route('http://remote.test/remote/reveal/approve', async route => { approvalArrived(); await release; await route.fulfill({ status: 200, body: 'SYNTHETIC_SECRET' }).catch(() => {}); approvalFinished(); });
  await page.route('http://remote.test/remote/session', route => route.fulfill({ contentType: 'application/json', body: '{"remainingMs":60000}' }));
  await page.goto('http://remote.test/remote/reveal/review');
  await page.addScriptTag({content:'window.inlineViolation = true'}).catch(() => {});
  expect(await page.evaluate(() => window.inlineViolation)).toBeUndefined();
  await page.locator('#approve').click();
  await approved;
  await page.locator('#clear').click();
  await page.evaluate(() => window.watchSecrets());
  releaseApproval();
  await approvalDone;
  await page.evaluate(() => window.settleFetch('/remote/reveal/approve'));
  await expect(page.locator('#values')).toBeEmpty();
  expect(await page.evaluate(() => window.secretAfterClear)).toBe(false);

  // A fresh page: hiding then showing while status is pending must also invalidate it.
  let releaseStatus, statusArrived, statusFinished;
  const statusDone = new Promise(resolve => statusFinished = resolve);
  const statusStarted = new Promise(resolve => statusArrived = resolve);
  const statusRelease = new Promise(resolve => releaseStatus = resolve);
  await page.unroute('http://remote.test/remote/reveal/approve');
  await page.unroute('http://remote.test/remote/session');
  await page.route('http://remote.test/remote/reveal/approve', route => route.fulfill({ status: 200, body: 'SYNTHETIC_SECRET' }));
  await page.route('http://remote.test/remote/session', async route => { statusArrived(); await statusRelease; await route.fulfill({ contentType: 'application/json', body: '{"remainingMs":60000}' }).catch(() => {}); statusFinished(); });
  await page.reload();
  await page.locator('#approve').click();
  await statusStarted;
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')); Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')); });
  await page.evaluate(() => window.watchSecrets());
  releaseStatus();
  await statusDone;
  await page.evaluate(() => window.settleFetch('/remote/session'));
  await expect(page.locator('#values')).toBeEmpty();
  expect(await page.evaluate(() => window.secretAfterClear)).toBe(false);

  await page.unroute('http://remote.test/remote/session');
  await page.route('http://remote.test/remote/session', route => route.fulfill({ contentType: 'application/json', body: '{"remainingMs":60000}' }));
  await page.reload();
  await page.locator('#approve').click();
  await expect(page.locator('#values')).toHaveText('SYNTHETIC_SECRET');
  const storage = await page.evaluate(() => JSON.stringify({ local: {...localStorage}, session: {...sessionStorage}, cookie: document.cookie }));
  expect(await page.evaluate(() => document.cookie)).toBe('');
  expect(storage).not.toContain('SYNTHETIC_SECRET');
  await page.locator('#clear').click();
  await expect(page.locator('#values')).toBeEmpty();

  // Delayed body parsing cannot undo hide/show even when abort cannot stop it.
  await page.reload();
  await page.evaluate(() => {
    const original=window.fetch; window.bodyReached=new Promise(resolve=>window.markBody=resolve); window.bodyGate=new Promise(resolve=>window.releaseBody=resolve); window.bodyFinished=new Promise(resolve=>window.markFinished=resolve);
    window.fetch=async (...args)=>{const response=await original(...args); if(String(args[0]).endsWith('/remote/session')) {const json=response.json.bind(response); response.json=async()=>{window.markBody(); await window.bodyGate; try{return await json()}finally{window.markFinished()}}} return response};
  });
  await page.locator('#approve').click();
  await page.evaluate(() => window.bodyReached);
  await page.evaluate(() => {Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));window.watchSecrets();window.releaseBody()});
  await page.evaluate(() => window.bodyFinished);
  await page.evaluate(() => window.settleFetch('/remote/session'));
  await expect(page.locator('#values')).toBeEmpty();
  expect(await page.evaluate(() => window.secretAfterClear)).toBe(false);

  // A polling response arriving after clear cannot re-display values.
  await page.unroute('http://remote.test/remote/session');
  let pollReached, releasePoll, pollFinished;
  const pollStarted=new Promise(resolve=>pollReached=resolve), pollGate=new Promise(resolve=>releasePoll=resolve), pollDone=new Promise(resolve=>pollFinished=resolve);
  let requests=0;
  await page.route('http://remote.test/remote/session', async route=>{if(++requests>1){pollReached();await pollGate; await route.fulfill({contentType:'application/json',body:'{"remainingMs":60000}'}).catch(()=>{}); pollFinished(); return} await route.fulfill({contentType:'application/json',body:'{"remainingMs":60000}'})});
  await page.reload();
  await page.locator('#approve').click();
  await expect(page.locator('#values')).toHaveText('SYNTHETIC_SECRET');
  await pollStarted;
  await page.locator('#clear').click();
  await page.evaluate(() => window.watchSecrets());
  releasePoll();
  await pollDone;
  await page.evaluate(() => window.settleFetch('/remote/session'));
  await expect(page.locator('#values')).toBeEmpty();
  expect(await page.evaluate(() => window.secretAfterClear)).toBe(false);

  // A status response delayed past its claimed lifetime must never display data.
  await page.unroute('http://remote.test/remote/session');
  let staleArrived, releaseStale;
  const staleStarted = new Promise(resolve => staleArrived = resolve);
  const staleRelease = new Promise(resolve => releaseStale = resolve);
  await page.route('http://remote.test/remote/session', async route => { staleArrived(); await staleRelease; await route.fulfill({contentType:'application/json', body:'{"remainingMs":1050}'}).catch(() => {}); });
  await page.reload();
  await page.evaluate(() => { window.secretEverShown = false; new MutationObserver(() => { if(document.querySelector('#values').textContent.includes('SYNTHETIC_SECRET')) window.secretEverShown = true; }).observe(document.querySelector('#values'), {childList:true, subtree:true, characterData:true}); });
  await page.locator('#approve').click();
  await staleStarted;
  // The server claims only 50ms after the conservative one-second margin.
  await page.waitForTimeout(150);
  releaseStale();
  await expect(page.locator('#values')).toHaveText('Reveal unavailable');
  expect(await page.evaluate(() => window.secretEverShown)).toBe(false);

  // Simulate OS sleep: monotonic clock paused while wall clock advances.
  await page.unroute('http://remote.test/remote/session');
  let sleepArrived, releaseSleep;
  const sleepStarted=new Promise(resolve=>sleepArrived=resolve), sleepGate=new Promise(resolve=>releaseSleep=resolve);
  await page.route('http://remote.test/remote/session',async route=>{sleepArrived();await sleepGate;await route.fulfill({contentType:'application/json',body:'{"remainingMs":5000}'})});
  await page.reload();
  await page.evaluate(() => {let wall=100000; Date.now=()=>wall; window.advanceWall=()=>{wall+=10000}; Object.defineProperty(performance,'now',{configurable:true,value:()=>2000}); window.watchSecrets()});
  await page.locator('#approve').click();
  await sleepStarted;
  await page.evaluate(() => window.advanceWall());
  releaseSleep();
  await expect(page.locator('#values')).toHaveText('Reveal unavailable');
  await page.evaluate(() => window.settleFetch('/remote/session'));
  expect(await page.evaluate(() => window.secretAfterClear)).toBe(false);
});

for (const mode of ['javascript-disabled', 'csp-blocked']) {
  test(`enabled reveal has no native approval fallback when ${mode}`, async ({ browser }) => {
    const fixture = path.join(os.tmpdir(), `brine-reveal-no-fallback-${process.pid}-${mode}.json`);
    const context = await browser.newContext({ javaScriptEnabled: mode !== 'javascript-disabled' });
    try {
      execFileSync('go', ['test', './internal/remoteui', '-run', '^TestRevealBrowserFixture$', '-count=1'], { env: { ...process.env, BRINE_REVEAL_FIXTURE: fixture } });
      const data = JSON.parse(fs.readFileSync(fixture, 'utf8'));
      let approvals = 0;
      await context.route('http://remote.test/**', route => {
        if (route.request().url().endsWith('/remote/reveal/approve')) {
          approvals++;
          return route.fulfill({ status: 200, body: 'SYNTHETIC_SECRET' });
        }
        return route.fulfill({ contentType: 'text/html', headers: { 'content-security-policy': mode === 'csp-blocked' ? "default-src 'none'; script-src 'none'" : data.csp }, body: data.html });
      });
      const page = await context.newPage();
      await page.goto('http://remote.test/remote/reveal/review');
      await expect(page.locator('form')).toHaveCount(0);
      await page.locator('#approve').click();
      await expect(page.locator('#values')).toBeEmpty();
      expect(approvals).toBe(0);
      expect(page.url()).toBe('http://remote.test/remote/reveal/review');
    } finally {
      await context.close();
      if (fs.existsSync(fixture)) fs.unlinkSync(fixture);
    }
  });
}
