const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Synthetic read-only fixture only; no engine');

test('action change discards stale preview and disconnect never replays', async ({page}) => {
  const id = 'br_' + 'c'.repeat(32); let previews = 0, applies = 0;
  await page.route('**/api/stacks/observability/operations/preview', async route => {
    previews++;
    const action = route.request().postDataJSON().action;
    if (action === 'up') await new Promise(resolve => setTimeout(resolve, 350));
    await route.fulfill({status: 200, headers: {'Content-Type':'application/json','X-Brine-Request-ID':id}, body: JSON.stringify({requestId:id, reviewDigest:'digest', confirmationToken:'token-'+action, review:{intent:{action}, preview:{result:{resources:[], coverage:{network:{count:1,complete:true}}, errors:[]}}}})});
  });
  await page.route('**/api/stacks/observability/stop', async route => { applies++; await route.abort('connectionreset'); });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.addScriptTag({url:new URL('/static/attended.js',baseURL).href});
  await page.evaluate(() => { document.getElementById('action-output').dataset.attended='true'; void runAction('up','observability'); void runAction('stop','observability'); });
  await expect(page.locator('#action-log')).toContainText('"action": "stop"');
  await page.waitForTimeout(500);
  await expect(page.locator('#action-log')).not.toContainText('"action": "up"');
  await page.locator('#attended-confirm input').check();
  await page.getByRole('button',{name:'Confirm once'}).click();
  await expect(page.locator('#action-log')).toContainText('Delivery unknown');
  await expect(page.getByRole('button',{name:'Confirm once'})).toBeDisabled();
  expect(previews).toBe(2); expect(applies).toBe(1);
});

test('failed preview never exposes a confirmation', async ({page}) => {
  let applies = 0;
  await page.route('**/api/stacks/observability/operations/preview', route => route.fulfill({status:503,body:'unavailable'}));
  await page.route('**/api/stacks/observability/up', route => { applies++; return route.abort(); });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.addScriptTag({url:new URL('/static/attended.js',baseURL).href});
  await page.evaluate(() => { document.getElementById('action-output').dataset.attended='true'; return runAction('up','observability'); });
  await expect(page.locator('#action-log')).toContainText('Preview unavailable');
  await expect(page.getByRole('button',{name:'Confirm once'})).toHaveCount(0);
  expect(applies).toBe(0);
});
