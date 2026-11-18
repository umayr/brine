const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('container detail keeps labels and log controls usable on mobile', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 700 });
  await page.goto(new URL('/containers/grafana', baseURL).href);
  await expect(page.getByRole('heading', { name: 'Labels' })).toBeVisible();
  await expect(page.getByText('dill.stack', { exact: true })).toBeVisible();
  await expect(page.getByRole('searchbox', { name: 'filter' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'reload' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
});

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
  await expect(page.locator('#container-action-uncertain')).toContainText('could not be confirmed');
  await expect(page.locator('#container-action-uncertain')).toBeVisible();
});

test('lost action response remains unconfirmed across an inventory refresh', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 850 });
  let requests = 0;
  await page.route('**/web/containers/*/stop', route => { requests++; return route.abort(); });
  await page.goto(new URL('/containers', baseURL).href);
  const body = page.locator('#containers-body');
  await body.getByRole('button', { name: 'Stop', exact: true }).first().click();
  const warning = page.locator('#container-action-uncertain');
  await expect(warning).toBeVisible();
  await expect(warning).toContainText('Inventory may be stale');
  const refresh = warning.getByRole('button', { name: 'Refresh containers' });
  await refresh.focus();
  await expect(refresh).toBeFocused();
  await page.screenshot({ path: testInfo.outputPath('container-uncertain.png') });
  await page.keyboard.press('Enter');
  await expect(warning).toBeVisible();
  await expect(body.locator('tr').first().locator('td')).toHaveCount(7);
  expect(requests).toBe(1);
});

test('mobile container actions retain their target and support keyboard scrolling', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(new URL('/containers', baseURL).href);
  const region = page.getByRole('region', { name: 'Containers table, scroll horizontally for all columns and actions' });
  await expect(page.getByText('Scroll horizontally to see all columns and actions.')).toBeVisible();
  await expect(region.getByRole('button', { name: 'Stop', exact: true }).first()).toBeVisible();
  expect(await region.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true);
  await region.focus();
  for (let i = 0; i < 30; i++) await page.keyboard.press('ArrowRight');
  const row = region.locator('tbody tr').first();
  const stop = row.getByRole('button', { name: 'Stop', exact: true });
  await stop.focus();
  await expect(stop).toBeFocused();
  await expect(stop).toHaveAccessibleDescription(/.+/);
  await expect(row.locator('.container-action-name')).toBeVisible();
  await expect.poll(async () => {
    const box = await stop.boundingBox(); const view = await region.boundingBox();
    return box.x >= view.x && box.x + box.width <= view.x + view.width + 1;
  }).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('container-actions.png') });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('action failure is reported even if polling replaces its row', async ({ page }) => {
  let release, started;
  const held = new Promise(resolve => { release = resolve; });
  const begun = new Promise(resolve => { started = resolve; });
  await page.route('**/web/containers/*/stop', async route => {
    started(); await held; await route.fulfill({ status: 503 });
  });
  await page.goto(new URL('/containers', baseURL).href);
  const body = page.locator('#containers-body');
  const stop = body.getByRole('button', { name: 'Stop', exact: true }).first();
  await stop.click(); await begun;
  const refresh = page.waitForResponse(response => response.url().endsWith('/web/containers'));
  await page.evaluate(() => htmx.ajax('GET', '/web/containers', { target: '#containers-body', swap: 'innerHTML' }));
  await refresh;
  release();
  await expect(page.locator('#container-action-uncertain')).toBeVisible();
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
