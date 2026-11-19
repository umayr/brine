const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!base, 'Read-only fixture required');

test('mobile navigation shows the current section on arrival without moving focus', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(base + '/recipes');
  const nav = page.getByRole('navigation', { name: 'Main navigation' });
  const current = nav.getByRole('link', { name: 'Snippets', exact: true });
  await expect(current).toHaveAttribute('aria-current', 'page');
  await expect.poll(async () => {
    const bar = await nav.boundingBox();
    const link = await current.boundingBox();
    return link.x >= bar.x - 1 && link.x + link.width <= bar.x + bar.width + 1;
  }).toBe(true);
  expect(await page.evaluate(() => [window.scrollX, window.scrollY])).toEqual([0, 0]);
  await expect(page.locator('body')).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to main content' })).toBeFocused();
});

for (const width of [390, 1440]) {
  test(`keyboard skip link reaches main content at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 850 });
    await page.goto(base + '/stacks');
    await page.keyboard.press('Tab');
    const skip = page.getByRole('link', { name: 'Skip to main content' });
    await expect(skip).toBeFocused();
    const box = await skip.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    await page.screenshot({ path: testInfo.outputPath('skip-link.png') });
    await page.keyboard.press('Enter');
    await expect(page.locator('main')).toBeFocused();
    await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Stacks', exact: true })).toHaveAttribute('aria-current', 'page');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  });
}
