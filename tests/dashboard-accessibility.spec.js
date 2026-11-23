const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

for (const colorScheme of ['light', 'dark']) {
  for (const width of [320, 390, 768, 1440]) {
    test(`dashboard hierarchy and keyboard actions at ${width}px in ${colorScheme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 850 });
      await page.emulateMedia({ colorScheme });
      await page.goto(base + '/');
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme);
      await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
      const attention = page.getByRole('region', { name: 'Needs attention' });
      const link = attention.getByRole('link', { name: 'Inspect container: jellyfin' });
      await expect(link).toBeVisible();
      await link.focus();
      await expect(link).toBeFocused();
      await expect(link).toHaveCSS('outline-style', 'solid');
      expect(Math.abs((await link.boundingBox()).height - 36)).toBeLessThan(0.01);
      expect(await link.evaluate(el => getComputedStyle(el).backgroundColor === getComputedStyle(el.closest('section')).borderInlineStartColor)).toBe(true);
      const metrics = page.getByRole('region', { name: 'Inventory' });
      await expect(metrics.getByRole('link', { name: /Containers/ })).toHaveAttribute('href', '/containers');
      await expect(metrics.getByText('Reported by engine')).toBeVisible();
      const operations = page.getByRole('region', { name: 'Recent operations' });
      const observed = page.getByRole('region', { name: 'Last observed' });
      await expect(operations).toContainText('aggregate request history is not available');
      await expect(observed.locator('time')).toHaveCount(3);
      expect((await observed.boundingBox()).y - ((await operations.boundingBox()).y + (await operations.boundingBox()).height)).toBeGreaterThan(24);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath('dashboard.png'), fullPage: true });
      await page.keyboard.press('Enter');
      await expect(page.getByRole('heading', { name: 'jellyfin', exact: true })).toBeVisible();
      await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Containers' })).toHaveAttribute('href', '/containers');
    });
  }
}
