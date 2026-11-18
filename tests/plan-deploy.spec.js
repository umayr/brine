const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Set BRINE_FIXTURE_BASE_URL to run populated fixture checks');

for (const failure of ['network', 'http']) {
  test(`failed plan clears prior results and reports unavailable: ${failure}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 850 });
    let requests = 0;
    await page.route('**/web/stacks/observability/plan', route => {
      if (++requests === 1) return route.fulfill({ body: '<div>Prior plan</div>', contentType: 'text/html' });
      return failure === 'network' ? route.abort() : route.fulfill({ status: 503, body: 'untrusted failure' });
    });
    await page.goto(new URL('/stacks/observability', baseURL).href);
    const plan = page.locator('#plan-button');
    const result = page.locator('#plan-result');
    await plan.click();
    await expect(result).toHaveText('Prior plan');
    await plan.focus();
    await page.keyboard.press('Enter');
    await expect(result).toHaveText('Plan unavailable. No current plan was returned.');
    await expect(result).toHaveAttribute('role', 'status');
    await expect(result).not.toContainText('Prior plan');
    await page.screenshot({ path: testInfo.outputPath('plan-unavailable.png') });
  });
}

test('a newer plan request supersedes an older response', async ({ page }) => {
  let release, started, requests = 0;
  const held = new Promise(resolve => { release = resolve; });
  const begun = new Promise(resolve => { started = resolve; });
  await page.route('**/web/stacks/observability/plan', async route => {
    if (++requests === 1) {
      started(); await held;
      return route.fulfill({ body: '<div>Stale plan</div>', contentType: 'text/html' }).catch(() => {});
    }
    return route.fulfill({ body: '<div>Current plan</div>', contentType: 'text/html' });
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const plan = page.locator('#plan-button');
  await plan.click(); await begun;
  await plan.click();
  await expect(page.locator('#plan-result')).toHaveText('Current plan');
  release();
  await expect(page.locator('#plan-result')).toHaveText('Current plan');
});

test('new plan request shows progress instead of an earlier plan', async ({ page }) => {
  let release, requests = 0;
  const held = new Promise(resolve => { release = resolve; });
  await page.route('**/web/stacks/observability/plan', async route => {
    if (++requests === 1) return route.fulfill({ body: '<div>Prior plan</div>', contentType: 'text/html' });
    await held;
    await route.fulfill({ body: '<div>Current plan</div>', contentType: 'text/html' });
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const plan = page.locator('#plan-button');
  await plan.click();
  await expect(page.locator('#plan-result')).toHaveText('Prior plan');
  await plan.click();
  await expect(page.locator('#plan-result')).toHaveText('Generating plan…');
  release();
  await expect(page.locator('#plan-result')).toHaveText('Current plan');
});

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
