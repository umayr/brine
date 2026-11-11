const { test, expect } = require('@playwright/test');

test('editor keeps in-flight diagnostics and formatting tied to the submitted draft', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const editor = page.locator('.cm-content');
  await expect(editor).toBeVisible();
  let release;
  await page.route('**/web/stacks/observability/validate', async route => {
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({status: 200, contentType: 'text/html', body: '<div>Draft validated</div>'});
  });
  await page.locator('#validate-btn').click();
  await expect(page.locator('#edit-result')).toHaveText('Validating draft…');
  await expect(page.locator('#validate-btn')).toBeDisabled();
  await editor.click(); await page.keyboard.type('new');
  release();
  await expect(page.locator('#edit-result')).toContainText('changed during validation');
  await page.route('**/web/stacks/observability/fmt', async route => {
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({status: 200, contentType: 'text/plain', body: 'formatted'});
  });
  release = null;
  await page.locator('#fmt-btn').click();
  await expect(page.locator('#edit-result')).toHaveText('Formatting draft…');
  await editor.click(); await page.keyboard.type('later');
  release();
  await expect(page.locator('#edit-result')).toContainText('not applied');
  await expect(editor).toContainText('later');
});
