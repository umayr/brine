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

test('failed container action does not replace inventory with an inventory error', async ({ page }) => {
  await page.route('**/web/containers/*/stop', route => route.fulfill({ status: 503 }));
  await page.goto(new URL('/containers', baseURL).href);
  const body = page.locator('#containers-body');
  const stop = body.getByRole('button', { name: 'Stop', exact: true }).first();
  await expect(stop).toBeVisible();
  const rowCount = await body.locator('tr').count();
  await stop.evaluate(button => {
    window.actionFinished = new Promise(resolve => {
      button.addEventListener('htmx:afterRequest', event => resolve(event.detail.xhr.status), { once: true });
    });
  });
  await stop.click();
  expect(await page.evaluate(() => window.actionFinished)).toBe(503);
  await expect(stop).toBeVisible();
  await expect(body.locator('tr')).toHaveCount(rowCount);
  await expect(body).not.toContainText('Inventory is unavailable');
});

test('retry supersedes an in-flight poll instead of letting an older failure replace inventory', async ({ page }) => {
  let requests = 0;
  let releasePoll;
  let pollStarted;
  const pending = new Promise(resolve => { releasePoll = resolve; });
  const started = new Promise(resolve => { pollStarted = resolve; });
  await page.route('**/web/containers', async route => {
    const request = ++requests;
    if (request === 1) return route.fulfill({ status: 503 });
    if (request === 2) {
      pollStarted();
      await pending;
      return route.fulfill({ status: 503 }).catch(() => {});
    }
    return route.continue();
  });
  await page.goto(new URL('/containers', baseURL).href);
  const body = page.locator('#containers-body');
  await expect(body.getByRole('button', { name: 'Retry' })).toBeVisible();
  await body.evaluate(element => {
    window.pollAborted = new Promise(resolve => {
      element.addEventListener('htmx:beforeRequest', event => {
        if (event.detail.elt === element) event.detail.xhr.addEventListener('abort', () => resolve(true), { once: true });
      }, { once: true });
    });
  });
  await started;
  await body.getByRole('button', { name: 'Retry' }).click();
  expect(await page.evaluate(() => window.pollAborted)).toBe(true);
  releasePoll();
  await expect(body.locator('tr').first().locator('td')).toHaveCount(7);
  await expect(body).not.toContainText('Inventory is unavailable');
});
