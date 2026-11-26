const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!base, 'Read-only fixture required');

for (const width of [320, 390, 1440]) {
  test(`target details remain readable and keyboard accessible at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 850 });
    await page.goto(base + '/stacks');
    const details = page.locator('.runtime-details');
    const summary = details.locator('summary');
    const body = details.locator('.runtime-details-body');
    await expect(summary).toContainText('configured only');
    await expect(body).toBeHidden();
    await summary.focus();
    await page.keyboard.press('Enter');
    await expect(details).toHaveAttribute('open', '');
    await expect(body).toBeVisible();
    await expect(body).toContainText('Socket:');
    await expect(body).not.toContainText(/legacy/i);
    await expect(body).toContainText('Configured only');
    // A long socket must wrap rather than be hidden by the shell.
    await body.locator('span').first().evaluate(el => { el.textContent = '/a-very-long-synthetic-socket-path/'.repeat(5); });
    const rect = await body.boundingBox();
    expect(rect.x).toBeGreaterThanOrEqual(0);
    expect(rect.x + rect.width).toBeLessThanOrEqual(width);
    expect(await body.evaluate(el => el.scrollWidth <= el.clientWidth)).toBeTruthy();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
    await page.screenshot({ path: testInfo.outputPath('target-expanded.png') });
    await page.keyboard.press('Space');
    await expect(body).toBeHidden();
  });
}
