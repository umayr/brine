const { test, expect } = require('@playwright/test');

test('existing nested local module can be browsed with saved validation guidance', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const file = 'modules/settings & local.pkl';
  const moduleLink = page.getByRole('link', { name: file });
  await moduleLink.focus();
  await page.keyboard.press('Enter');
  await expect(moduleLink).toHaveAttribute('aria-current', 'page');
  const label = `Pkl file contents: ${file}`;
  const editor = page.locator('.cm-content');
  await expect(editor).toHaveText('value = 1');
  await editor.focus();
  await expect(editor).toBeFocused();
  await expect(editor).toHaveAttribute('role', 'textbox');
  await expect(editor).toHaveAccessibleName(label);
  await expect(page.getByText(label, { exact: true })).toBeVisible();
  await expect(page.getByText('Nested module', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Delete file' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Set as entrypoint' })).toHaveCount(0);
  await expect(page.locator('#validate-btn')).toBeDisabled();
  await expect(page.locator('#fmt-btn')).toBeDisabled();
  await expect(page.getByText('Save this module, then validate the saved entrypoint', { exact: false })).toBeVisible();
});
