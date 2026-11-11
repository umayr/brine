const { test, expect } = require('@playwright/test');

// Opt in against a separately started, disposable Brine instance. Never run
// lifecycle actions: the configured engine may be a real one.
const baseURL = process.env.BRINE_VISUAL_BASE_URL;
const password = process.env.BRINE_VISUAL_PASSWORD;
test.skip(!baseURL || !password, 'Set BRINE_VISUAL_BASE_URL and BRINE_VISUAL_PASSWORD to run visual checks');

for (const theme of ['light', 'dark']) {
for (const viewport of [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 768, height: 1024 },
  { name: 'mobile', width: 390, height: 844 },
]) {
  test(`capture and inspect core pages: ${viewport.name} ${theme}`, async ({ browser }, testInfo) => {
    const context = await browser.newContext({ viewport: { width: viewport.width, height: viewport.height } });
    await context.addInitScript(value => localStorage.setItem('theme', value), theme);
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(new URL('/login', baseURL).href);
    await page.screenshot({ path: testInfo.outputPath('login.png'), fullPage: true });
    await page.locator('input[name="user"]').fill('admin');
    await page.locator('input[name="password"]').fill(password);
    await page.locator('form').first().evaluate(form => form.submit());
    await page.waitForURL(url => url.pathname === '/');
    for (const [name, route] of [['dashboard', '/'], ['stacks', '/stacks'], ['new-stack', '/stacks/new'], ['snippets', '/recipes'], ['settings', '/settings']]) {
      await page.goto(new URL(route, baseURL).href);
      await expect(page.locator('main')).toBeVisible();
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme === 'dark' ? 'dark' : 'light');
      if (name === 'settings') await expect(page.locator('#theme-select')).toHaveValue(theme);
      if (name === 'new-stack') {
        const editor = page.locator('#nf-panels .cm-editor').first();
        await expect(editor).toBeVisible();
        expect((await editor.boundingBox()).width, 'editor must remain usable on narrow screens').toBeGreaterThan(250);
      }
      await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
      const overflow = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: window.innerWidth }));
      expect(overflow.width, `${name} has page-level horizontal overflow`).toBeLessThanOrEqual(overflow.viewport + 1);
    }
    expect(errors, 'uncaught browser errors').toEqual([]);
    await context.close();
  });
}
}


test('theme switching preserves editor contents and preference', async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: 'dark' });
  const page = await context.newPage();
  await page.goto(new URL('/login', baseURL).href);
  await page.locator('input[name="user"]').fill('admin');
  await page.locator('input[name="password"]').fill(password);
  await page.locator('form').first().evaluate(form => form.submit());
  await page.waitForURL(url => url.pathname === '/');
  await page.goto(new URL('/stacks/new', baseURL).href);
  const editor = page.locator('.cm-content').first();
  await editor.click();
  await page.keyboard.type('theme-test');
  const textarea = page.locator('#nf-panels textarea[name="filecontent"]').first();
  const before = await textarea.inputValue();
  // Change settings in another tab so the editor is never navigated away or
  // replaced. The production storage event must update its theme in place.
  const settings = await context.newPage();
  await settings.goto(new URL('/settings', baseURL).href);
  await settings.locator('#theme-select').selectOption('light');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(textarea).toHaveValue(before);
  await settings.locator('#theme-select').selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(textarea).toHaveValue(before);
  await settings.reload();
  await expect(settings.locator('#theme-select')).toHaveValue('dark');
  await settings.locator('#theme-select').selectOption('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await context.close();
});
