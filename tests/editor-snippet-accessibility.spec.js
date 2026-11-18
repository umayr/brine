const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

for (const path of ['/stacks/homepage/edit', '/stacks/new']) {
  test(`snippet controls are named and keyboard usable on ${path}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 850 });
    await page.route('**/api/recipes/*', route => route.fulfill({ json: { body: '// fixture snippet' } }));
    await page.goto(new URL(path, baseURL).href);
    const insert = page.getByRole('button', { name: 'Insert PostgreSQL Database at cursor' });
    await expect(insert).toBeVisible();
    await expect(insert).toHaveCSS('opacity', '1');
    await expect(page.getByRole('link', { name: 'New snippet' })).toBeVisible();
    await insert.focus();
    await expect(insert).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath('snippet-control.png') });
    await page.keyboard.press('Enter');
    await expect(page.locator('.cm-content').first()).toContainText('// fixture snippet');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

test('editor exposes active file and keyboard-accessible add file dialog', async ({ page }) => {
  await page.goto(new URL('/stacks/homepage/edit', baseURL).href);
  await expect(page.locator('a[href*="/edit?file="][aria-current="page"]')).toHaveCount(1);
  const add = page.getByRole('button', { name: 'Add file' });
  await add.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Add file' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel('Filename')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(add).toBeFocused();
});

test('failed snippet fetch reports an error without changing the draft', async ({ page }) => {
  await page.route('**/api/recipes/*', route => route.fulfill({ status: 503 }));
  await page.goto(new URL('/stacks/homepage/edit', baseURL).href);
  const editor = page.locator('.cm-content');
  const original = await editor.innerText();
  await page.getByRole('button', { name: 'Insert PostgreSQL Database at cursor' }).click();
  await expect(page.locator('#toasts')).toContainText('Failed to load snippet');
  expect(await editor.innerText()).toBe(original);
});
