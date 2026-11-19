const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

test('the populated dashboard Stacks table is named from its heading', async ({ page }) => {
  await page.goto(new URL('/', base).href);
  const heading = page.getByRole('heading', { name: 'Stacks', exact: true });
  await expect(heading).toBeVisible();
  const table = page.getByRole('table').filter({ has: page.getByRole('link', { name: 'View stack homepage', exact: true }) });
  await expect(table).toBeVisible();
  await expect(table).toHaveAccessibleName('Stacks');
});

test('the populated dashboard Running containers table is named from its heading', async ({ page }) => {
  await page.goto(new URL('/', base).href);
  await expect(page.getByRole('heading', { name: 'Running containers', exact: true })).toBeVisible();
  const region = page.getByRole('region', { name: 'Running containers table', exact: true });
  const table = region.getByRole('table');
  await expect(table.getByRole('link', { name: 'grafana', exact: true })).toBeVisible();
  await expect(table).toHaveAccessibleName('Running containers');
});

test('the overflowing dashboard container table can be scrolled by keyboard', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(new URL('/', base).href);
  const scroller = page.getByRole('table').filter({ has: page.getByRole('columnheader', { name: 'Image', exact: true }) }).locator('..');
  expect(await scroller.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true);
  await page.getByRole('link', { name: 'View all containers' }).focus();
  await page.keyboard.press('Tab');
  await expect(scroller).toBeFocused();
  await expect(scroller).toHaveRole('region');
  await expect(scroller).toHaveAccessibleName('Running containers table');
  await expect(scroller).toHaveCSS('outline-style', 'solid');
  await page.keyboard.press('ArrowRight');
  await expect.poll(() => scroller.evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
  await page.keyboard.press('Tab');
  await expect(scroller.getByRole('link', { name: 'grafana', exact: true })).toBeFocused();
});

for (const width of [390, 1440]) {
  test(`overview links identify their destination at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 850 });
    await page.goto(new URL('/', base).href);
    await expect(page.getByRole('link', { name: 'View all stacks' })).toHaveAttribute('href', '/stacks');
    await expect(page.getByRole('link', { name: 'View all containers' })).toHaveAttribute('href', '/containers');
    const link = page.getByRole('link', { name: 'View stack homepage' });
    await expect(link).toHaveAttribute('href', '/stacks/homepage');
    await link.focus();
    await expect(link).toBeFocused();
    await expect(page.getByText('reported by engine')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath('overview.png') });
  });
}
