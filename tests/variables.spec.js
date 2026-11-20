const { test, expect } = require('@playwright/test');

const base = process.env.BRINE_FIXTURE_BASE_URL || 'http://127.0.0.1:18082';

test('shared variables have their own navigation and stack panel shows inheritance', async ({ page }) => {
  await page.goto(`${base}/variables`);
  await expect(page.getByRole('heading', { name: 'Variables', exact: true })).toBeVisible();
  await page.locator('.desk-more summary').click();
  await expect(page.getByRole('link', { name: 'Variables', exact: true })).toHaveAttribute('aria-current', 'page');
  await page.locator('.desk-more summary').press('Escape');
  await expect(page.getByText('BRINE_VAR_APP_DOMAIN', { exact: true })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Value for APP_DOMAIN' })).toHaveValue('example.test');
  await page.goto(`${base}/stacks/homepage`);
  const panel = page.locator('#variables');
  await expect(panel.getByRole('heading', { name: 'Variables' })).toBeVisible();
  await expect(panel.getByText('Stack override')).toBeVisible();
  await expect(panel.getByText('Shared value: example.test')).toBeVisible();
  await expect(panel.getByText('Inherited from shared')).toBeVisible();
  await expect(panel.getByText('BRINE_VAR_TIMEZONE')).toBeVisible();
  const inherited = panel.getByRole('textbox', { name: 'Inherited value for TIMEZONE' });
  const override = panel.getByRole('textbox', { name: 'Value for APP_DOMAIN' });
  await expect(inherited).toBeDisabled();
  await expect(inherited).toHaveValue('UTC');
  await expect(inherited).toHaveAttribute('title', /disabled.*shared variable/i);
  await expect(override).toHaveValue('homepage.example.test');
  const rows = panel.locator('.variables-row');
  await expect(rows.first()).toContainText('Inherited from shared');
  await expect(rows.nth(1)).toContainText('Stack override');
  const inheritedBox = await inherited.boundingBox();
  const overrideBox = await override.boundingBox();
  expect(Math.abs(inheritedBox.x - overrideBox.x)).toBeLessThan(1);
  expect(Math.abs(inheritedBox.width - overrideBox.width)).toBeLessThan(1);
  const saveBox = await panel.getByRole('button', { name: 'Save', exact: true }).boundingBox();
  const removeBox = await panel.getByRole('button', { name: 'Delete stack value for APP_DOMAIN' }).boundingBox();
  expect(Math.abs(saveBox.y - removeBox.y)).toBeLessThan(1);
  await expect(page.locator('[data-stack-action]').first()).toHaveText(/Deploy/);
  await page.goto(`${base}/settings`);
  await expect(page.getByRole('link', { name: 'Shared variables' })).toHaveCount(0);
});

test('stack panel uses the updated stylesheet even if the old URL is stale', async ({ page }) => {
  await page.route('**/static/vendor/app.css*', async route => {
    const url = new URL(route.request().url());
    if (!url.search) return route.fulfill({ status: 200, contentType: 'text/css', body: '/* stale stylesheet */' });
    return route.continue();
  });
  await page.goto(`${base}/stacks/homepage`);
  const href = await page.locator('link[rel="stylesheet"][href*="/static/vendor/app.css"]').getAttribute('href');
  expect(new URL(href, base).search).not.toBe('');
  const panel = page.locator('#variables');
  const styles = await panel.locator('.variables-panel-header').evaluate(el => ({ padding: parseFloat(getComputedStyle(el).paddingLeft), display: getComputedStyle(el).display }));
  expect(styles.padding).toBeGreaterThan(0);
  expect(styles.display).toBe('flex');
  await expect(panel.locator('.variables-add-form')).toHaveCSS('display', 'grid');
});

test('variable controls fit a narrow viewport', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${base}/stacks/homepage`);
  await expect(page.locator('#variables')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.goto(`${base}/variables`);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
