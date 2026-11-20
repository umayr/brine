const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

for (const kind of ['containers', 'images']) {
  test(`${kind}: search, filter, sorting, pagination and refresh work together`, async ({ page }) => {
    const containers = kind === 'containers';
    let count = 24;
    await page.route(`**/web/${kind}`, route => route.fulfill({
      contentType: 'text/html',
      body: Array.from({ length: count }, (_, i) => {
        const name = `example-${String(count - i).padStart(2, '0')}`;
        const filter = i % 2 === 0 ? (containers ? 'running' : 'tagged') : (containers ? 'exited' : 'untagged');
        return `<tr id="row-${name}" data-inventory-row data-name="${name}" data-filter="${filter}"><td><a href="#${name}">${name}</a></td><td>Actions</td></tr>`;
      }).join(''),
    }));
    await page.goto(`${base}/${kind}`);
    const search = page.getByRole('searchbox', { name: `Search ${kind}` });
    const next = page.getByRole('button', { name: 'Next', exact: true });
    const visible = page.locator('[data-inventory-row]:visible');
    const summary = page.locator('[data-inventory-summary]');
    await expect(summary).toHaveText('Showing 1–10 of 24 · Page 1 of 3');
    await page.getByLabel('Sort').selectOption('name');
    await expect(visible.first()).toContainText('example-01');
    await page.getByLabel('Filter').selectOption(containers ? 'running' : 'tagged');
    await expect(summary).toHaveText('Showing 1–10 of 12 · Page 1 of 2');
    await next.focus();
    await page.keyboard.press('Enter');
    await expect(next).toBeFocused();
    await expect(next).toHaveAttribute('aria-disabled', 'true');
    await expect(summary).toHaveText('Showing 11–12 of 12 · Page 2 of 2');
    count = 8;
    await page.evaluate(async k => { await htmx.ajax('GET', `/web/${k}`, { target: `#${k}-body`, swap: 'innerHTML' }); }, kind);
    await expect(summary).toHaveText('Showing 1–4 of 4 · Page 1 of 1');
    await expect(page.getByLabel('Filter')).toHaveValue(containers ? 'running' : 'tagged');
    await search.fill('not-present');
    await expect(summary).toContainText('No matching results');
    await expect(visible).toHaveCount(0);
    await search.fill('example-04');
    await expect(visible).toHaveCount(1);
  });
}

test('inventory row and action heights, outlined states, and separate stack columns', async ({ page }) => {
  await page.goto(`${base}/containers`);
  const rows = page.locator('#containers-body [data-inventory-row]');
  await expect(rows).toHaveCount(4);
  expect(await rows.evaluateAll(els => els.map(el => el.getBoundingClientRect().height))).toEqual([52, 52, 52, 52]);
  const actions = rows.locator('.ui-button');
  expect(await actions.evaluateAll(els => Array.from(new Set(els.map(el => el.getBoundingClientRect().height))))).toEqual([36]);
  await expect(rows.first().locator('.ui-status')).toHaveCSS('border-top-style', 'solid');
  await page.goto(`${base}/stacks`);
  const table = page.getByRole('table', { name: 'Stacks' });
  for (const name of ['Source', 'Runtime', 'Containers']) await expect(table.getByRole('columnheader', { name })).toBeVisible();
  await expect(table.getByRole('row').filter({ hasText: 'observability' })).toContainText('2 of 2 running');
});

test('keyboard focus in the container table survives background polling', async ({ page }) => {
  await page.goto(`${base}/containers`);
  const link = page.getByRole('link', { name: 'grafana', exact: true });
  await link.focus();
  await expect(link).toBeFocused();
  await page.evaluate(() => new Promise(resolve => setTimeout(resolve, 5200)));
  await expect(link).toBeFocused();
});
