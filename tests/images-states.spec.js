const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('image inventory table has a name independent of its scroll region', async ({ page }) => {
  await page.goto(new URL('/images', baseURL).href);
  const region = page.getByRole('region', { name: 'Images table, scroll horizontally for all columns and actions' });
  await expect(region.getByRole('cell', { name: 'grafana/grafana:11.4', exact: true })).toBeVisible();
  await expect(region.getByRole('table')).toHaveAccessibleName('Images');
});

test('images inventory shows loading, empty and failed request states with retry', async ({ page }) => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  let requests = 0;
  await page.route('**/web/images', async route => {
    requests++;
    if (requests === 1) {
      await pending;
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<tr><td colspan="5">No images found.</td></tr>' });
    } else if (requests === 2) {
      await route.abort();
    } else {
      await route.continue();
    }
  });
  await page.goto(new URL('/images', baseURL).href);
  await expect(page.getByRole('status').filter({ hasText: 'Loading images' })).toBeVisible();
  release();
  await expect(page.locator('#images-body')).toContainText('No images found.');
  await page.evaluate(() => htmx.trigger('#images-body', 'refresh-images'));
  await expect(page.getByRole('alert')).toContainText('Inventory is unavailable');
  await page.getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#images-body')).toContainText('grafana/grafana');
});

test('successful last image removal hides stale actions while inventory refreshes', async ({ page }) => {
  let releaseRefresh;
  const pending = new Promise(resolve => { releaseRefresh = resolve; });
  let requests = 0;
  await page.route('**/web/images', async route => {
    if (++requests === 1) return route.continue();
    await pending;
    await route.fulfill({ status: 200, contentType: 'text/html', body: '<tr><td colspan="5">No images found.</td></tr>' });
  });
  await page.route('**/api/images/**', route => route.fulfill({ status: 204 }));
  await page.goto(new URL('/images', baseURL).href);
  await expect(page.locator('#images-body')).toContainText('grafana/grafana');
  await page.locator('#images-body tr').evaluateAll(rows => rows.slice(1).forEach(row => row.remove()));
  await page.getByRole('button', { name: 'Remove', exact: true }).click();
  await page.locator('#modal-confirm-btn').click();
  await expect(page.locator('#images-body').getByRole('status')).toHaveText('Loading images…');
  await expect(page.locator('#images-body').getByRole('button', { name: 'Remove' })).toHaveCount(0);
  await expect(page.locator('#images-body')).not.toContainText('grafana/grafana');
  releaseRefresh();
  await expect(page.locator('#images-body')).toContainText('No images found.');
});

for (const failure of ['network', 'http']) {
  test(`uncertain image removal remains visible and offers an explicit refresh: ${failure}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 850 });
    let deletes = 0, inventories = 0;
    await page.route('**/api/images/**', route => {
      deletes++;
      return failure === 'network' ? route.abort() : route.fulfill({ status: 503 });
    });
    await page.route('**/web/images', route => { inventories++; return route.continue(); });
    await page.goto(new URL('/images', baseURL).href);
    const body = page.locator('#images-body');
    await expect(body).toContainText('grafana/grafana');
    await body.getByRole('button', { name: 'Remove', exact: true }).first().click();
    await page.locator('#modal-confirm-btn').click();
    const warning = page.locator('#image-action-uncertain');
    await expect(warning).toBeVisible();
    await expect(warning).toContainText('could not be confirmed');
    await expect(warning).toContainText('Inventory may be stale');
    expect(deletes).toBe(1);
    expect(inventories).toBe(1);
    const refresh = warning.getByRole('button', { name: 'Refresh images' });
    await refresh.focus();
    await expect(refresh).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath('uncertain-removal.png') });
    await page.keyboard.press('Enter');
    await expect.poll(() => inventories).toBe(2);
    await expect(warning).toBeVisible();
    expect(deletes).toBe(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

test('mobile image table supports keyboard scrolling and identifies removal target', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(new URL('/images', baseURL).href);
  const region = page.getByRole('region', { name: 'Images table, scroll horizontally for all columns and actions' });
  await expect(page.getByText('Scroll horizontally to see all columns and actions.')).toBeVisible();
  await expect(region).toContainText('grafana/grafana');
  expect(await region.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true);
  await region.focus();
  for (let i = 0; i < 20; i++) await page.keyboard.press('ArrowRight');
  await expect.poll(() => region.evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
  const row = region.locator('tbody tr').filter({ hasText: 'grafana/grafana' });
  const button = row.getByRole('button', { name: 'Remove' });
  await button.focus();
  await expect(button).toBeFocused();
  await expect.poll(async () => {
    const box = await button.boundingBox();
    const view = await region.boundingBox();
    return box.x >= view.x && box.x + box.width <= view.x + view.width + 1;
  }).toBe(true);
  await expect(button).toHaveAccessibleDescription(/.+/);
  await expect(row.locator('.image-action-name')).toContainText('grafana/grafana');
  await page.screenshot({ path: testInfo.outputPath('image-actions.png') });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('HTTP inventory error offers retry', async ({ page }) => {
  let requests = 0;
  await page.route('**/web/images', route => {
    if (++requests === 1) return route.fulfill({ status: 503, body: 'unavailable' });
    return route.continue();
  });
  await page.goto(new URL('/images', baseURL).href);
  await expect(page.locator('#images-body').getByRole('alert')).toContainText('Inventory is unavailable');
  await page.locator('#images-body').getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#images-body')).toContainText('grafana/grafana');
});

test('engine inventory error offers retry', async ({ page }) => {
  let requests = 0;
  await page.route('**/web/images', route => {
    if (++requests === 1) return route.fulfill({ status: 200, contentType: 'text/html', body: '<tr><td colspan="5"><div role="alert">Images could not be loaded. Inventory is unavailable.</div><button hx-get="/web/images" hx-target="#images-body" hx-swap="innerHTML">Retry</button></td></tr>' });
    return route.continue();
  });
  await page.goto(new URL('/images', baseURL).href);
  await expect(page.locator('#images-body').getByRole('alert')).toContainText('Inventory is unavailable');
  await page.locator('#images-body').getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#images-body')).toContainText('grafana/grafana');
});
