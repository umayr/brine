const { test, expect } = require('@playwright/test');

test('add file keeps validation and server errors visible in the dialog', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const dialog = page.locator('#new-file-modal');
  const filename = dialog.locator('[name=filename]');
  let requests = 0;
  await page.route('**/web/stacks/observability/files', route => {
    requests++;
    return route.fulfill({ status: 200, contentType: 'text/html', body: '<div>Unable to create file</div>' });
  });
  await page.getByTitle('Add file').click();
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).toBeVisible();
  expect(await filename.evaluate(el => el.validity.valueMissing)).toBe(true);
  await filename.fill('invalid.txt');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).toBeVisible();
  expect(await filename.evaluate(el => el.validity.patternMismatch)).toBe(true);
  expect(requests).toBe(0);
  await filename.fill('shared.pkl');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.locator('#new-file-result')).toHaveText('Unable to create file');
  await expect(dialog).toBeVisible();
  expect(requests).toBe(1);
  await expect(filename).toHaveValue('shared.pkl');
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).not.toBeVisible();
});

test('HTTP and network failures are visible without rendering response HTML', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const dialog = page.locator('#new-file-modal');
  let requests = 0;
  await page.route('**/web/stacks/observability/files', route => {
    requests++;
    if (requests === 1) return route.fulfill({ status: 403, contentType: 'text/html', body: '<img src=x onerror="window.injected=true">' });
    return route.abort('failed');
  });
  await page.getByTitle('Add file').click();
  await dialog.locator('[name=filename]').fill('shared.pkl');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.locator('#new-file-result')).toContainText('HTTP 403');
  await expect(dialog.locator('#new-file-result img')).toHaveCount(0);
  expect(await page.evaluate(() => window.injected)).toBeUndefined();
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.locator('#new-file-result')).toContainText('Check your connection');
  await expect(dialog.locator('[name=filename]')).toHaveValue('shared.pkl');
  await expect(dialog).toBeVisible();
});

test('successful file creation follows the server redirect', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  await page.route('**/web/stacks/observability/files', route => route.fulfill({
    status: 200, headers: { 'HX-Redirect': '/stacks/observability/edit?file=shared.pkl' }, body: '',
  }));
  await page.getByTitle('Add file').click();
  await page.locator('#new-file-name').fill('shared.pkl');
  await page.locator('#new-file-modal').getByRole('button', { name: 'Create' }).click();
  await expect(page).toHaveURL(/\/stacks\/observability\/edit\?file=shared\.pkl$/);
});
