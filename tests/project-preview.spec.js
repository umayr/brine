const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL || 'http://127.0.0.1:18082';

test('read-only project preview retains stack controls and explains its boundaries', async ({ page }) => {
  await page.goto(`${base}/stacks/media`);
  const section = page.locator('#project-preview');
  await expect(section).toContainText('contents can contain secrets');
  for (const warning of ['.git are omitted', 'Symlinks and special files are rejected', 'Ignored, untracked and hidden files are included', '.gitignore is not honored', '.env and keys may be included', 'hard links', 'mounted subdirectories', 'not detected or filtered', 'root ancestors', 'same user must be trusted', 'not a filesystem sandbox', 'Empty directories and file modes are not preserved', 'not an atomic snapshot', 'does not transfer files or authorize deployment']) await expect(section).toContainText(warning);
  await section.getByRole('button', { name: 'Preview project files' }).click();
  await expect(section.locator('#project-preview-result')).toContainText('bytes');
  await expect(section.locator('#project-preview-result')).toContainText('compose.pkl');
  await expect(section.locator('#project-preview-result')).toContainText('<unsafe>.txt');
  await expect(section.locator('#project-preview-result')).toContainText('Transfer unsupported');
  await expect(section.locator('#project-preview-result')).toContainText('versioned provisional contract');
  await expect(section.locator('#project-preview-result')).toContainText('revalidated by the server');
  await expect(section.locator('#project-preview-result unsafe')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Deploy', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Variables', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'File secret references' })).toBeVisible();
  await section.getByRole('button', { name: 'Project snapshot preview (read-only)' }).click();
  await expect(page.locator('#project-preview-content')).toBeHidden();
  await expect(page.locator('#stack-variables-content')).toBeVisible();
});

test('preview renders hostile filenames as text and displays safe errors', async ({ page }) => {
  await page.route('**/web/stacks/media/project-preview', async route => route.fulfill({status: 200, contentType: 'text/html', body: '<div>&lt;img src=x onerror=alert(1)&gt; — 3 bytes</div>'}));
  await page.goto(`${base}/stacks/media`);
  await page.getByRole('button', { name: 'Preview project files' }).click();
  await expect(page.locator('#project-preview-result')).toContainText('<img src=x onerror=alert(1)>');
  await expect(page.locator('#project-preview-result img')).toHaveCount(0);
  await page.unroute('**/web/stacks/media/project-preview');
  await page.route('**/web/stacks/media/project-preview', async route => route.fulfill({status: 422, body: 'private path'}));
  await page.getByRole('button', { name: 'Preview project files' }).click();
  await expect(page.locator('#project-preview-result')).toContainText('Project preview unavailable');
  await expect(page.locator('#project-preview-result')).not.toContainText('private path');
});
