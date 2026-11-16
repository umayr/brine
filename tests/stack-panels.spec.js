const { test, expect } = require('@playwright/test');

const base = process.env.BRINE_FIXTURE_BASE_URL || 'http://127.0.0.1:18082';

test('stack panels collapse independently without hiding source or operation controls', async ({ page }) => {
  await page.goto(`${base}/stacks/media`);
  const variables = page.getByRole('button', { name: 'Variables', exact: true });
  const secrets = page.getByRole('button', { name: 'File secret references' });
  const containers = page.getByRole('button', { name: 'Containers', exact: true });
  const git = page.getByRole('button', { name: 'Git source' });
  for (const button of [variables, secrets, containers, git]) {
    await expect(button).toHaveAttribute('aria-expanded', 'true');
  }

  await variables.click();
  await expect(variables).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('#stack-variables-content')).toBeHidden();
  await expect(page.locator('#secret-bindings-content')).toBeVisible();
  await expect(page.locator('.pk-panel:not(.hidden)')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Deploy', exact: true })).toBeVisible();

  await secrets.focus();
  await page.keyboard.press('Space');
  await expect(secrets).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('#secret-bindings-content')).toBeHidden();
  await page.keyboard.press('Enter');
  await expect(page.locator('#secret-bindings-content')).toBeVisible();

  await containers.click();
  await expect(page.locator('#stack-containers-panel')).toBeHidden();
  // An inventory refresh can update its child without expanding the panel.
  await page.locator('#stack-containers').evaluate(el => { el.textContent = 'Updated inventory'; });
  await expect(page.locator('#stack-containers-panel')).toBeHidden();
  await containers.click();
  await expect(page.locator('#stack-containers')).toContainText('Updated inventory');

  await git.click();
  await expect(git).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('#git-source-panel')).toBeHidden();
  await expect(page.locator('#git-source-actions')).toBeHidden();
  await git.click();
  await expect(page.locator('#git-source-panel')).toBeVisible();
  await expect(page.locator('#git-source-actions')).toBeVisible();
  await expect(variables).toHaveAttribute('aria-expanded', 'false');
  await variables.click();
  await expect(page.locator('#stack-variables-content')).toBeVisible();
  await expect(page.locator('.pk-panel:not(.hidden)')).toBeVisible();
});
