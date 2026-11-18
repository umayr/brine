const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

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
