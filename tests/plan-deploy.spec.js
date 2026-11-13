const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Set BRINE_FIXTURE_BASE_URL to run populated fixture checks');

for (const outcome of ['success', 'failure']) {
  test(`deploy clears the previous plan even on ${outcome}`, async ({ page }) => {
    await page.route('**/web/stacks/observability/plan', route => route.fulfill({ body: '<div>Previous plan</div>', contentType: 'text/html' }));
    let release;
    const held = new Promise(resolve => { release = resolve; });
    await page.route('**/api/stacks/observability/up', async route => {
      await held;
      await route.fulfill(outcome === 'success'
        ? { status: 200, body: '[brine:done]' }
        : { status: 500, body: 'deployment failed' });
    });
    await page.goto(new URL('/stacks/observability', baseURL).href);
    const plan = page.locator('#plan-button');
    await plan.click();
    await expect(page.locator('#plan-result')).toHaveText('Previous plan');
    await page.getByRole('button', { name: 'Deploy', exact: true }).click();
    await expect(page.locator('#plan-result')).toBeEmpty();
    await expect(plan).toBeDisabled();
    release();
    await expect(page.locator('#action-status')).toHaveText(outcome === 'success' ? 'complete' : 'failed');
    await expect(plan).toBeEnabled();
    await expect(page.locator('#plan-result')).toBeEmpty();
  });
}

test('restart clears the previous plan even when it fails', async ({ page }) => {
  await page.route('**/web/stacks/observability/plan', route => route.fulfill({ body: '<div>Previous plan</div>', contentType: 'text/html' }));
  await page.route('**/api/stacks/observability/restart', route => route.fulfill({ status: 500, body: 'restart failed' }));
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.locator('#plan-button').click();
  await expect(page.locator('#plan-result')).toHaveText('Previous plan');
  await page.getByRole('button', { name: 'Restart', exact: true }).click();
  await expect(page.locator('#action-status')).toHaveText('failed');
  await expect(page.locator('#plan-result')).toBeEmpty();
});

test('deploy cancels an in-flight plan so its stale response cannot appear', async ({ page }) => {
  let release;
  const held = new Promise(resolve => { release = resolve; });
  let started;
  const planStarted = new Promise(resolve => { started = resolve; });
  await page.route('**/web/stacks/observability/plan', async route => {
    started();
    await held;
    await route.fulfill({ body: '<div>Late stale plan</div>', contentType: 'text/html' }).catch(() => {});
  });
  await page.route('**/api/stacks/observability/up', route => route.fulfill({ status: 500, body: 'deployment failed' }));
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.evaluate(() => {
    window.planAborted = new Promise(resolve => {
      document.getElementById('plan-button').addEventListener('htmx:beforeRequest', event => {
        event.detail.xhr.addEventListener('abort', resolve, { once: true });
      }, { once: true });
    });
  });
  await page.locator('#plan-button').click();
  await planStarted;
  await page.getByRole('button', { name: 'Deploy', exact: true }).click();
  await page.evaluate(() => window.planAborted);
  await expect(page.locator('#action-status')).toHaveText('failed');
  release();
  await expect(page.locator('#plan-result')).toBeEmpty();
});
