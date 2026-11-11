const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('stack inventory loading, empty, network failure and retry', async ({ page }) => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  let requests = 0;
  await page.route('**/web/stacks', async route => {
    if (++requests === 1) { await pending; return route.continue(); }
    if (requests === 2) return route.abort();
    return route.continue();
  });
  await page.goto(new URL('/stacks', baseURL).href);
  const inventory = page.locator('#stack-inventory');
  await expect(inventory.getByRole('status')).toHaveText('Loading stacks…');
  release();
  await expect(inventory.getByRole('link', { name: 'Deploy' })).toHaveCount(0);
  await expect(inventory.getByRole('button', { name: 'Deploy' }).first()).toBeVisible();
  await page.evaluate(() => htmx.trigger('#stack-inventory', 'refresh-stacks'));
  await expect(inventory.getByRole('alert')).toContainText('Inventory is unavailable');
  await inventory.getByRole('button', { name: 'Retry' }).click();
  await expect(inventory.getByRole('button', { name: 'Deploy' }).first()).toBeVisible();
});

test('stack HTTP failure and retry supersede an in-flight poll', async ({ page }) => {
  let requests = 0, releasePoll, pollStarted;
  const pending = new Promise(resolve => { releasePoll = resolve; });
  const started = new Promise(resolve => { pollStarted = resolve; });
  await page.route('**/web/stacks', async route => {
    if (++requests === 1) return route.fulfill({ status: 503 });
    if (requests === 2) { pollStarted(); await pending; return route.fulfill({ status: 503 }).catch(() => {}); }
    return route.continue();
  });
  await page.goto(new URL('/stacks', baseURL).href);
  const inventory = page.locator('#stack-inventory');
  await expect(inventory.getByRole('button', { name: 'Retry' })).toBeVisible();
  await page.evaluate(() => htmx.trigger('#stack-inventory', 'refresh-stacks'));
  await started;
  await inventory.getByRole('button', { name: 'Retry' }).click();
  releasePoll();
  await expect(inventory.getByRole('button', { name: 'Deploy' }).first()).toBeVisible();
  await expect(inventory).not.toContainText('Inventory is unavailable');
});

test('empty inventory and successful deletion refresh count and empty state', async ({ page }) => {
  let requests = 0;
  await page.route('**/web/stacks', route => ++requests === 1 ? route.continue() : route.fulfill({status: 200, contentType: 'text/html', body: '<div>0 TOTAL</div><p>No stacks yet</p>'}));
  await page.route('**/api/stacks/*', route => route.fulfill({status: 204}));
  await page.goto(new URL('/stacks', baseURL).href);
  const inventory = page.locator('#stack-inventory');
  await inventory.getByRole('button', { name: 'Delete' }).first().click();
  await page.locator('#modal-confirm-btn').click();
  await expect(inventory).toContainText('0 TOTAL');
  await expect(inventory).toContainText('No stacks yet');
});
