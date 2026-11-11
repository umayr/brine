const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('container inventory loading, empty, request failure and retry', async ({ page }) => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  let requests = 0;
  await page.route('**/web/containers', async route => {
    if (++requests === 1) {
      await pending;
      return route.fulfill({ status: 200, contentType: 'text/html', body: '<tr><td colspan="7">No containers found.</td></tr>' });
    }
    if (requests === 2) return route.abort();
    return route.continue();
  });
  await page.goto(new URL('/containers', baseURL).href);
  await expect(page.locator('#containers-body').getByRole('status')).toHaveText('Loading containers…');
  release();
  await expect(page.locator('#containers-body')).toContainText('No containers found.');
  await expect(page.locator('#containers-body').getByRole('alert')).toContainText('Inventory is unavailable', { timeout: 8000 });
  await page.locator('#containers-body').getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#containers-body tr').first().locator('td')).toHaveCount(7);
});

test('container inventory HTTP failure offers retry', async ({ page }) => {
  let requests = 0;
  await page.route('**/web/containers', route => ++requests === 1 ? route.fulfill({ status: 503 }) : route.continue());
  await page.goto(new URL('/containers', baseURL).href);
  await expect(page.locator('#containers-body').getByRole('alert')).toContainText('Inventory is unavailable');
  await page.locator('#containers-body').getByRole('button', { name: 'Retry' }).click();
  await expect(page.locator('#containers-body tr').first().locator('td')).toHaveCount(7);
});
