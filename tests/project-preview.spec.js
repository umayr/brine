const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;
const staging = process.env.BRINE_STAGING_BASE_URL;

test('ordinary stacks do not show transfer or an empty remote-request panel', async ({ page }) => {
  await page.goto(base+'/stacks/media');
  await expect(page.locator('#project-preview')).toHaveCount(0);
  await expect(page.locator('.stack-overview')).not.toContainText('Configured only');
  await expect(page.locator('.stack-overview')).toContainText('Local');
  await page.getByRole('button', { name: 'Restore', exact: true }).click();
  await expect(page.locator('#stack-source-history')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Remote requests' })).toHaveCount(0);
});

test('project transfer shows a gated read-only preview and safe errors without Plan', async ({ page }) => {
  test.skip(!staging, 'synthetic staging fixture not configured');
  await page.goto(staging+'/login');
  await page.locator('input[name="user"]').fill('admin');
  await page.locator('input[name="password"]').fill('fixture-only-not-a-secret');
  await page.getByRole('button', { name: /sign in/i }).click();
  await page.goto(staging+'/stacks/homepage');
  await page.getByRole('button', { name: 'Project transfer' }).click();
  const section=page.locator('#project-preview');
  await expect(section.getByText('Preview scope and limits',{exact:true})).toBeVisible();
  await section.getByText('Preview scope and limits',{exact:true}).click();
  await expect(section).toContainText('not an atomic snapshot');
  const preview=section.getByRole('button',{name:'Preview project files'});
  await preview.click();
  await expect(section.locator('#project-preview-result')).toContainText('compose.pkl');
  await page.route('**/web/stacks/homepage/project-preview',r=>r.fulfill({contentType:'text/html',body:'<div>&lt;img src=x onerror=alert(1)&gt; — 3 bytes</div>'}));
  await preview.click();
  await expect(section.locator('#project-preview-result')).toContainText('<img src=x onerror=alert(1)>');
  await expect(section.locator('#project-preview-result img')).toHaveCount(0);
  await page.unroute('**/web/stacks/homepage/project-preview');
  await page.route('**/web/stacks/homepage/project-preview', r=>r.fulfill({status:422,body:'private path'}));
  await preview.click();
  await expect(section.locator('#project-preview-result')).toContainText('Project preview unavailable');
  await expect(section.locator('#project-preview-result')).not.toContainText('private path');
});
