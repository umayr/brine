const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_VISUAL_BASE_URL;
const password = process.env.BRINE_VISUAL_PASSWORD;
test.skip(!baseURL || !password, 'Requires a disposable authenticated fixture');

for (const theme of ['light', 'dark']) {
  for (const width of [390, 1440]) {
    test(`sign-in is usable at ${width}px in ${theme} mode`, async ({ browser }) => {
      const context = await browser.newContext({ viewport: { width, height: 900 } });
      await context.addInitScript(value => localStorage.setItem('theme', value), theme);
      const page = await context.newPage();
      await page.goto(new URL('/login?next=%2Fsettings', baseURL).href);
      await expect(page.getByRole('main')).toBeVisible();
      await expect(page.getByRole('heading', { name: 'Welcome back.' })).toBeVisible();
      const user = page.getByLabel('Username');
      const pass = page.getByLabel('Password');
      await expect(user).toBeFocused();
      await expect(page.locator('input[name="next"]')).toHaveValue('/settings');
      await expect(user).toHaveAttribute('autocomplete', 'username');
      await expect(pass).toHaveAttribute('autocomplete', 'current-password');
      await expect(user).toHaveValue('admin');
      await page.keyboard.press('Tab');
      await expect(pass).toBeFocused();
      await page.keyboard.type('wrong');
      await page.keyboard.press('Enter');
      await expect(page.getByRole('alert')).toHaveText('Invalid username or password.');
      await expect(user).toHaveValue('admin');
      await expect(pass).toHaveValue('');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
      await context.close();
    });
  }
}

test('successful first sign-in returns to the requested destination', async ({ page }) => {
  await page.goto(new URL('/login?next=%2Fsettings', baseURL).href);
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page).toHaveURL(new URL('/settings', baseURL).href);
});
