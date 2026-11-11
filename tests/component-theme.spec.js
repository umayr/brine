const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Set BRINE_FIXTURE_BASE_URL to a read-only fixture');

async function openFixture(page, route = '/') {
  const response = await page.goto(new URL(route, baseURL).href);
  expect(response.status()).toBe(200);
  await expect(page.locator('main')).toBeVisible();
}

test('theme, system preference, migration and accents are independent', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('component-test-initialized')) {
      localStorage.setItem('theme', 'night');
      sessionStorage.setItem('component-test-initialized', 'yes');
    }
  });
  await openFixture(page, '/settings');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('#theme-select')).toHaveValue('dark');
  await page.evaluate(() => localStorage.setItem('theme', 'corporate'));
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.locator('#theme-select')).toHaveValue('light');
  await page.locator('#theme-select').selectOption('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  for (const accent of ['sage', 'glacier', 'lilac', 'sand', 'rose', 'graphite']) {
    await page.locator(`label:has(input[name="brine-accent"][value="${accent}"])`).click();
    await expect(page.locator('html')).toHaveAttribute('data-accent', accent);
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  }
  await page.locator('#theme-select').selectOption('dark');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'graphite');
});

test('live tokens affect controls without changing semantic status', async ({ page }) => {
  await openFixture(page);
  // Local probes exercise the production stylesheet without triggering fixture actions.
  await page.evaluate(() => {
    const probe = document.createElement('div');
    probe.innerHTML = '<button class="ui-button ui-button-primary">Probe</button><span class="ui-status ui-status-success">Success</span>';
    document.body.append(probe);
  });
  const button = page.locator('.ui-button-primary').filter({ hasText: 'Probe' });
  const status = page.locator('.ui-status-success').filter({ hasText: 'Success' });
  const before = await status.evaluate(el => getComputedStyle(el).color);
  await page.evaluate(() => {
    const style = document.documentElement.style;
    style.setProperty('--color-accent', 'rgb(17, 34, 51)');
    style.setProperty('--radius-control', '13px');
    style.setProperty('--control-height', '53px');
  });
  await expect(button).toHaveCSS('background-color', 'rgb(17, 34, 51)');
  await expect(button).toHaveCSS('border-radius', '13px');
  await expect(button).toHaveCSS('height', '53px');
  await expect(status).toHaveCSS('color', before);
  expect(await status.evaluate(el => getComputedStyle(el, '::before').maskImage)).not.toBe('none');
});

test('native dialog focus, disabled controls and reduced motion', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await openFixture(page);
  const dialog = page.locator('#confirm-modal');
  await page.evaluate(() => {
    const trigger = document.createElement('button');
    trigger.id = 'component-test-trigger';
    trigger.textContent = 'Open';
    document.body.append(trigger);
    trigger.focus();
    document.documentElement.style.setProperty('--dialog-width', '640px');
    document.getElementById('confirm-modal').showModal();
  });
  await expect(dialog).toHaveJSProperty('open', true);
  await expect(dialog.locator('.ui-dialog-panel')).toHaveCSS('width', '640px');
  expect(await dialog.evaluate(el => el.contains(document.activeElement))).toBe(true);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveJSProperty('open', false);
  await expect(page.locator('#component-test-trigger')).toBeFocused();
  await page.evaluate(() => document.getElementById('confirm-modal').showModal());
  await dialog.click({ position: { x: 5, y: 5 } });
  await expect(dialog).toHaveJSProperty('open', false);
  await page.evaluate(() => {
    const button = document.createElement('button');
    button.className = 'ui-button'; button.disabled = true; button.id = 'component-test-disabled';
    document.body.append(button);
    const spinner = document.createElement('span');
    spinner.className = 'ui-spinner'; spinner.id = 'component-test-spinner';
    document.body.append(spinner);
  });
  await expect(page.locator('#component-test-spinner')).toHaveCSS('animation-name', 'none');
  await expect(page.locator('#component-test-disabled')).toBeDisabled();
  await page.locator('#component-test-disabled').evaluate(el => el.focus());
  await expect(page.locator('#component-test-disabled')).not.toBeFocused();
  expect(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true);
});

for (const width of [390, 1440]) {
  test(`fixture has no document overflow at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    for (const route of ['/', '/stacks', '/containers', '/images', '/settings']) {
      const response = await page.goto(new URL(route, baseURL).href);
      expect(response.status()).toBe(200);
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), route).toBeLessThanOrEqual(1);
    }
  });
}
