const { test, expect } = require('@playwright/test');

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
