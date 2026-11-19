const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

test('the Git Push dialog skips its invisible backdrop in the tab order', async ({ page }) => {
  await page.goto(new URL('/stacks/media', base).href);
  const opener = page.getByRole('button', { name: 'Push staged changes', exact: true });
  await opener.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Push changes', exact: true });
  await expect(dialog.getByRole('textbox', { name: 'Commit message', exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Push', exact: true })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(dialog.getByRole('button', { name: 'Close dialog', exact: true })).not.toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(opener).toBeFocused();
});

for (const failure of ['network', 'http']) {
  test(`Git pull reports an unconfirmed outcome on ${failure} failure`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 850 });
    let requests = 0;
    await page.route('**/web/stacks/media/git/pull', route => {
      requests++;
      return failure === 'network' ? route.abort() : route.fulfill({ status: 503, body: 'not a trusted diagnostic' });
    });
    await page.goto(new URL('/stacks/media', base).href);
    const pull = page.getByRole('button', { name: 'Pull latest' });
    await pull.focus();
    await page.keyboard.press('Enter');
    const result = page.locator('#git-result');
    await expect(result).toHaveAttribute('role', 'status');
    await expect(result).toContainText('Pull outcome could not be confirmed');
    await expect(result).toContainText('Inspect the working tree and revision before retrying');
    expect(requests).toBe(1);
    const pushBox = await page.getByRole('button', { name: 'Push staged changes' }).boundingBox();
    expect(pushBox.x + pushBox.width).toBeLessThanOrEqual(390);
    expect(await page.locator('.git-source-grid').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('git-pull-uncertain.png') });
  });
}

test('Git pull shows progress, prevents repeat while in flight, then displays the response', async ({ page }) => {
  let release, started, requests = 0;
  const waiting = new Promise(resolve => { release = resolve; });
  const start = new Promise(resolve => { started = resolve; });
  await page.route('**/web/stacks/media/git/pull', async route => {
    requests++; started(); await waiting;
    await route.fulfill({ status: 200, contentType: 'text/html', body: '<div>Already up to date (synthetic)</div>' });
  });
  await page.goto(new URL('/stacks/media', base).href);
  const pull = page.getByRole('button', { name: 'Pull latest' });
  await pull.click(); await start;
  await expect(pull).toBeDisabled();
  await expect(page.locator('#git-result')).toContainText('Pulling Git source');
  await pull.evaluate(el => el.click());
  expect(requests).toBe(1);
  release();
  await expect(page.locator('#git-result')).toContainText('Already up to date (synthetic)');
  await expect(pull).toBeEnabled();
});

test('Git settings have labelled inputs', async ({ page }) => {
  await page.goto(new URL('/stacks/media', base).href);
  const disclosure = page.locator('#git-source-panel summary');
  await expect(disclosure).toHaveAccessibleName('Git settings');
  await disclosure.focus();
  await page.keyboard.press('Enter');
  await expect(disclosure).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('textbox', { name: 'Branch', exact: true })).toBeFocused();
  for (const name of ['Branch', 'Folder', 'Poll interval']) await expect(page.getByRole('textbox', { name, exact: true })).toBeVisible();
});
