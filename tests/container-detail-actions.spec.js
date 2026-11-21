const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

test('container detail updates state and status from one action response', async ({ page }) => {
  let release, started, requests = 0;
  const held = new Promise(resolve => { release = resolve; });
  const begun = new Promise(resolve => { started = resolve; });
  await page.route('**/web/containers/*/start?view=state', async route => {
    requests++; started(); await held;
    await route.fulfill({ status: 200, contentType: 'text/html', body: '<span class="ui-status">running</span><div id="container-status" hx-swap-oob="innerHTML">Up just now</div>' });
  });
  await page.goto(new URL('/containers/grafana', base).href);
  const actions = page.locator('#container-detail-actions');
  await actions.getByRole('button', { name: 'Start', exact: true }).click(); await begun;
  await expect(actions.getByRole('button', { name: 'Stop' })).toBeDisabled();
  await actions.getByRole('button', { name: 'Stop' }).evaluate(el => el.click());
  expect(requests).toBe(1);
  release();
  await expect(page.locator('#container-state')).toHaveText('running');
  await expect(page.locator('#container-status')).toHaveText('Up just now');
  await expect(page.locator('#container-state tr')).toHaveCount(0);
  await expect(actions.getByRole('button', { name: 'Stop' })).toBeEnabled();
});

for (const failure of ['network', 'http']) {
  test(`container detail warns on ${failure} action failure`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 850 });
    let requests = 0;
    let release;
    const recovery = new Promise(resolve => { release = resolve; });
    await page.route('**/web/containers/*/stop?view=state', async route => {
      requests++;
      if (requests === 1) return failure === 'network' ? route.abort() : route.fulfill({ status: 502 });
      await recovery;
      await route.fulfill({ status: 200, contentType: 'text/html', body: '<span class="ui-status">exited</span><div id="container-status" hx-swap-oob="innerHTML">Exited just now</div>' });
    });
    await page.goto(new URL('/containers/grafana', base).href);
    const stop = page.locator('#container-detail-actions').getByRole('button', { name: 'Stop' });
    await stop.focus(); await page.keyboard.press('Enter');
    const warning = page.locator('#container-detail-uncertain');
    await expect(warning).toBeVisible();
    await expect(warning).toContainText('could not be confirmed');
    await expect(warning).toContainText('displayed state may be stale');
    await expect(warning.getByRole('link', { name: 'Reload details' })).toHaveAttribute('href', /\/containers\//);
    expect(requests).toBe(1);
    await page.screenshot({ path: testInfo.outputPath('container-detail-uncertain.png') });

    await stop.click();
    await expect(stop).toBeDisabled();
    await expect(warning).toBeVisible();
    release();
    await expect(warning).toBeHidden();
    await expect(page.locator('#container-state')).toHaveText('exited');
    await expect(page.locator('#container-status')).toHaveText('Exited just now');
    expect(requests).toBe(2);
  });
}
