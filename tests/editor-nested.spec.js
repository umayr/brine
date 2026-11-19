const { test, expect } = require('@playwright/test');

test('existing nested local module can be browsed with saved validation guidance', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  await page.getByRole('link', { name: 'modules/settings & local.pkl' }).click();
  await expect(page.locator('.cm-content')).toHaveText('value = 1');
  await expect(page.getByText('Nested module', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Delete file' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Set as entrypoint' })).toHaveCount(0);
  await expect(page.locator('#validate-btn')).toBeDisabled();
  await expect(page.locator('#fmt-btn')).toBeDisabled();
  await expect(page.getByText('Save this module, then validate the saved entrypoint', { exact: false })).toBeVisible();
});
