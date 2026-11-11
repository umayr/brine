const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

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
