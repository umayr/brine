const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
const endpoint = '**/web/stacks/observability/containers';

// The read-only fixture deliberately cannot execute Dill. Initial errors below
// use the actual route/partial; only successful recovery is intercepted. Go
// tests exercise the production success partial with a synthetic Dill process.
const empty = '<div class="px-5 py-8 text-center text-dim">No containers in this stack.</div>';
const populated = '<table><tbody><tr><td>recovered-web</td><td>running</td></tr></tbody></table>';

test('unavailable stack inventory has bounded keyboard retry and can recover to confirmed empty', async ({ page }) => {
  let requests = 0;
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  await page.route(endpoint, async route => {
    requests++;
    if (requests === 1) return route.continue();
    await pending;
    return route.fulfill({ contentType: 'text/html', body: empty });
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const inventory = page.locator('#stack-containers');
  await expect(inventory.getByRole('alert')).toHaveText('Stack container inventory is unavailable.');
  await expect(inventory).not.toContainText('No containers in this stack.');
  await expect(inventory).not.toContainText('/nonexistent-brine-fixture-dill');
  await expect(inventory).not.toContainText('lifecycle requires an explicit local engine endpoint');
  const retry = inventory.getByRole('button', { name: 'Retry stack container inventory' });
  await expect(retry).toHaveAttribute('hx-request', '{"timeout":10000}');
  await retry.focus();
  await expect(retry).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(retry).toBeDisabled();
  await page.keyboard.press('Enter');
  expect(requests).toBe(2);
  release();
  await expect(inventory).toContainText('No containers in this stack.');
  await expect(inventory.getByRole('alert')).toHaveCount(0);
  await expect(inventory.getByRole('button', { name: 'Retry stack container inventory' })).toHaveCount(0);
});

test('a timed-out retry becomes usable again without claiming the stack is empty', async ({ page }) => {
  let requests = 0;
  let recovered = false;
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  await page.route(endpoint, async route => {
    const request = ++requests;
    if (request === 1) return route.continue();
    if (request === 2) {
      await pending;
      return route.fulfill({ contentType: 'text/html', body: populated }).catch(() => {});
    }
    return recovered ? route.fulfill({ contentType: 'text/html', body: populated }) : route.continue();
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const inventory = page.locator('#stack-containers');
  const retry = inventory.getByRole('button', { name: 'Retry stack container inventory' });
  await expect(retry).toBeEnabled();
  await page.evaluate(() => {
    window.inventoryTimedOut = false;
    document.body.addEventListener('htmx:timeout', () => { window.inventoryTimedOut = true; }, { once: true });
  });
  await retry.click();
  await expect(retry).toBeDisabled();
  try {
    // Exercise the production ten-second bound, not a shortened test override.
    await expect.poll(() => page.evaluate(() => window.inventoryTimedOut), { timeout: 12000 }).toBe(true);
    await expect(retry).toBeEnabled();
    await expect(inventory.getByRole('alert')).toContainText('inventory is unavailable');
    await expect(inventory).not.toContainText('No containers in this stack.');
    recovered = true;
    await retry.click();
    await expect(inventory).toContainText('recovered-web');
  } finally {
    release();
  }
});

test('stack inventory refresh and polling retain an honest failure and recover to populated', async ({ page }) => {
  let mode = 'unavailable';
  let requests = 0;
  await page.route(endpoint, route => {
    requests++;
    return mode === 'unavailable' ? route.continue() : route.fulfill({ contentType: 'text/html', body: populated });
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const inventory = page.locator('#stack-containers');
  await expect(inventory.getByRole('alert')).toContainText('inventory is unavailable');
  // The existing five-second HTMX poll must still work after an error swap.
  await expect.poll(() => requests, { timeout: 8000 }).toBeGreaterThanOrEqual(2);
  await expect(inventory.getByRole('alert')).toBeVisible();
  await expect(inventory).not.toContainText('No containers in this stack.');
  mode = 'populated';
  await page.locator('#stack-containers-refresh').click();
  await expect(inventory).toContainText('recovered-web');
  await expect(inventory).toContainText('running');
  await expect(inventory.getByRole('alert')).toHaveCount(0);
  // A later source/target failure must not regress back to empty success.
  mode = 'unavailable';
  await page.locator('#stack-containers-refresh').click();
  await expect(inventory.getByRole('alert')).toContainText('inventory is unavailable');
  await expect(inventory).not.toContainText('No containers in this stack.');
  mode = 'populated';
  await inventory.getByRole('button', { name: 'Retry stack container inventory' }).click();
  await expect(inventory).toContainText('recovered-web');
});
