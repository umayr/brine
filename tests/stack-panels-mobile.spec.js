const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL || 'http://127.0.0.1:18082';

test('stack panel toggles work on a narrow screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${base}/stacks/homepage`);
  const toggle = page.getByRole('button', { name: 'Variables', exact: true });
  await toggle.click();
  await expect(page.locator('#stack-variables-content')).toBeHidden();
  await expect(page.locator('.pk-panel:not(.hidden)')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await toggle.click();
  await expect(page.locator('#stack-variables-content')).toBeVisible();
});
