const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('mobile stack table reveals its actions with keyboard scrolling', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(new URL('/stacks', baseURL).href);
  const region = page.getByRole('region', { name: 'Stacks table, scroll horizontally for all columns and actions' });
  await expect(page.getByText('Scroll horizontally to see all columns and actions.')).toBeVisible();
  await expect(region.getByRole('button', { name: 'Delete' }).first()).toBeAttached();
  expect(await region.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true);
  await region.focus();
  await expect(region).toBeFocused();
  for (let i = 0; i < 12; i++) await page.keyboard.press('ArrowRight');
  await expect.poll(() => region.evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
  const button = region.getByRole('button', { name: 'Delete' }).first();
  await button.focus();
  await expect(button).toBeFocused();
  await expect.poll(async () => {
    const box = await button.boundingBox();
    const view = await region.boundingBox();
    return box.x >= view.x && box.x + box.width <= view.x + view.width + 1;
  }).toBe(true);
  await expect(region.locator('tbody tr').first().locator('.stack-actions-name')).toHaveText('homepage');
  await expect(button).toHaveAccessibleDescription('homepage');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('stack-actions.png') });
  await region.focus();
  await region.evaluate(el => el.dataset.beforeRefresh = 'yes');
  await page.evaluate(() => htmx.trigger('#stack-inventory', 'refresh-stacks'));
  await expect(region).not.toHaveAttribute('data-before-refresh', 'yes');
  await expect(region).toBeFocused();
});

test('desktop keeps column labels and actions visible', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 850 });
  await page.goto(new URL('/stacks', baseURL).href);
  const region = page.getByRole('region', { name: 'Stacks table, scroll horizontally for all columns and actions' });
  await expect(region.getByRole('columnheader', { name: 'Updated' })).toBeVisible();
  await expect(region.getByRole('button', { name: 'Delete' }).first()).toBeVisible();
  await expect(page.getByText('Scroll horizontally to see all columns and actions.')).toBeHidden();
});
