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
