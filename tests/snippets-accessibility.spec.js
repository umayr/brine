const { test, expect } = require('@playwright/test');

for (const [path, value] of [['/recipes/new', ''], ['/recipes/simple-web-service/edit', 'Simple Web Service']]) {
  test(`the visible Name label names and focuses the textbox on ${path}`, async ({ page }) => {
    await page.goto(new URL(path, process.env.BRINE_FIXTURE_BASE_URL).href);
    const label = page.locator('label').filter({ hasText: /^Name$/ });
    await expect(label).toBeVisible();
    const name = page.getByRole('textbox', { name: 'Name', exact: true });
    await expect(name).toHaveValue(value);
    await label.click();
    await expect(name).toBeFocused();
  });
}

for (const { label, saved } of [
  { label: 'Category', saved: 'Web' },
  { label: 'Description', saved: 'Single HTTP service with port mapping' },
]) {
  for (const [path, value] of [['/recipes/new', ''], ['/recipes/simple-web-service/edit', saved]]) {
    test(`the visible ${label} label names and focuses the textbox on ${path}`, async ({ page }) => {
      await page.goto(new URL(path, process.env.BRINE_FIXTURE_BASE_URL).href);
      const visibleLabel = page.locator('label').filter({ hasText: new RegExp(`^${label}$`) });
      await expect(visibleLabel).toBeVisible();
      const field = page.getByRole('textbox', { name: label, exact: true });
      await expect(field).toHaveValue(value);
      await visibleLabel.click();
      await expect(field).toBeFocused();
    });
  }
}

test('snippet Edit links identify their targets without row context', async ({ page }) => {
  await page.goto(new URL('/recipes', process.env.BRINE_FIXTURE_BASE_URL).href);
  const table = page.getByRole('table');
  for (const [name, slug] of [['Simple Web Service', 'simple-web-service'], ['PostgreSQL Database', 'postgresql-database']]) {
    await expect(table.getByRole('cell', { name, exact: true })).toBeVisible();
    const edit = table.getByRole('link', { name: `Edit ${name}`, exact: true });
    await expect(edit).toHaveAttribute('href', `/recipes/${slug}/edit`);
    await expect(edit).toHaveText('Edit');
  }
});
