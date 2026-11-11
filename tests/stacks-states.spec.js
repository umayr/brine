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

test('in-flight deploy remains locked across inventory swaps and unlocks on completion', async ({ page }) => {
  let release, deploys = 0;
  const pending = new Promise(resolve => { release = resolve; });
  await page.route('**/api/stacks/*/up', async route => {
    deploys++;
    await pending;
    await route.fulfill({ status: 200, body: '[brine:done]' });
  });
  await page.goto(new URL('/stacks', baseURL).href);
  const inventory = page.locator('#stack-inventory');
  const row = inventory.locator('tr').filter({ has: page.getByRole('button', { name: 'Deploy', exact: true }) }).first();
  const name = await row.locator('[data-stack-action="deploy"]').getAttribute('data-stack-name');
  const deploy = inventory.locator('[data-stack-action="deploy"]').filter({ hasText: 'Deploy' }).first();
  await deploy.click();
  await expect(inventory.locator('[data-stack-action="deploy"]').first()).toHaveText('Deploying');
  await inventory.locator('[data-stack-action="deploy"]').first().evaluate(button => button.dataset.beforeRefresh = 'yes');
  const refreshed = page.waitForResponse(response => response.url().endsWith('/web/stacks'));
  await page.evaluate(() => htmx.trigger('#stack-inventory', 'refresh-stacks'));
  await refreshed;
  const current = inventory.locator('[data-stack-action="deploy"]').first();
  await expect(current).not.toHaveAttribute('data-before-refresh', 'yes');
  await expect(current).toHaveText('Deploying');
  await expect(current).toBeDisabled();
  await expect(inventory.locator('[data-stack-action="delete"]').first()).toBeDisabled();
  await page.evaluate(name => deployStack(name, null), name);
  expect(deploys).toBe(1);
  release();
  await expect(current).toHaveText('Deploy');
  await expect(current).toBeEnabled();
});

test('slow inventory response survives automatic polling ticks', async ({ page }) => {
  test.setTimeout(20000);
  let release, requests = 0;
  const pending = new Promise(resolve => { release = resolve; });
  await page.route('**/web/stacks', async route => {
    requests++;
    if (requests === 1) await pending;
    await route.continue().catch(() => {});
  });
  await page.goto(new URL('/stacks', baseURL).href);
  await page.waitForTimeout(5600);
  expect(requests).toBe(1);
  release();
  await expect(page.locator('#stack-inventory').getByRole('button', { name: 'Deploy' }).first()).toBeVisible();
});

test('deletion locks actions and supersedes a stale inventory request', async ({ page }) => {
  let releaseDelete, releasePoll, startedPoll, requests = 0, deletes = 0;
  const deletion = new Promise(resolve => { releaseDelete = resolve; });
  const poll = new Promise(resolve => { releasePoll = resolve; });
  const started = new Promise(resolve => { startedPoll = resolve; });
  await page.route('**/web/stacks', async route => {
    const request = ++requests;
    if (request === 2) { startedPoll(); await poll; return route.continue().catch(() => {}); }
    if (request >= 3) return route.fulfill({ status: 200, contentType: 'text/html', body: '<p>No stacks yet</p>' });
    return route.continue();
  });
  await page.route('**/api/stacks/*', async route => { deletes++; await deletion; await route.fulfill({status: 204}); });
  await page.goto(new URL('/stacks', baseURL).href);
  const inventory = page.locator('#stack-inventory');
  const deleteButton = inventory.locator('[data-stack-action="delete"]').first();
  await expect(deleteButton).toBeVisible();
  const name = await deleteButton.getAttribute('data-stack-name');
  await deleteButton.click();
  await page.locator('#modal-confirm-btn').click();
  await expect(deleteButton).toHaveText('Deleting');
  await page.evaluate(() => htmx.trigger('#stack-inventory', 'refresh-stacks'));
  await started;
  await page.evaluate(name => deleteStack(name, true), name);
  expect(deletes).toBe(1);
  releaseDelete();
  await expect(inventory).toContainText('No stacks yet');
  releasePoll();
  await expect(inventory).not.toContainText('Deleting');
});
