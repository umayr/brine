const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Set BRINE_FIXTURE_BASE_URL to run populated fixture checks');

for (const theme of ['light', 'dark']) {
  for (const viewport of [{ name: 'desktop', width: 1440, height: 900 }, { name: 'mobile', width: 390, height: 844 }]) {
    test(`populated pages: ${viewport.name} ${theme}`, async ({ browser }, testInfo) => {
      const context = await browser.newContext({ viewport: { width: viewport.width, height: viewport.height } });
      await context.addInitScript(value => localStorage.setItem('theme', value), theme);
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      for (const [name, route] of [['overview', '/'], ['stacks', '/stacks'], ['stack', '/stacks/observability'], ['editor', '/stacks/observability/edit'], ['containers', '/containers'], ['images', '/images'], ['settings', '/settings']]) {
        const response = await page.goto(new URL(route, baseURL).href);
        expect(response.status(), route).toBe(200);
        await expect(page.locator('main')).toBeVisible();
        if (name === 'overview') {
          await expect(page.locator('main')).toContainText('observability');
          await expect(page.locator('main')).toContainText('grafana');
        }
        if (name === 'containers') await expect(page.locator('#containers-body')).toContainText('grafana');
        if (name === 'images') await expect(page.locator('#images-body')).toContainText('grafana/grafana');
        await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
        expect(overflow, `${name} has horizontal page overflow`).toBeLessThanOrEqual(1);
      }
      expect(errors).toEqual([]);
      await context.close();
    });
  }
}

test('fixture refuses writes', async ({ request }) => {
  const response = await request.post(new URL('/api/stacks/observability/up', baseURL).href);
  expect(response.status()).toBe(405);
});
