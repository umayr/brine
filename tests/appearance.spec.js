const { test, expect } = require('@playwright/test');

const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Read-only fixture required');
const url = route => new URL(route, baseURL).href;
const accentCard = (page, value) => page.locator(`label:has(input[name="brine-accent"][value="${value}"])`);

test('Settings navigation exposes only functional browser-local appearance controls', async ({ page }) => {
  const mutations = [];
  page.on('request', request => { if (!['GET', 'HEAD'].includes(request.method())) mutations.push(request.url()); });
  await page.goto(url('/'));
  await expect(page.locator('aside #theme-select')).toHaveCount(0);
  await page.getByRole('link', { name: 'Settings', exact: true }).click();
  await expect(page).toHaveURL(url('/settings'));
  await expect(page.getByRole('heading', { name: 'Appearance', level: 1 })).toBeVisible();
  await expect(page.locator('aside a[aria-current="page"]')).toHaveAttribute('href', '/settings');
  await expect(page.getByRole('group', { name: 'Accent color' }).getByRole('radio')).toHaveCount(6);
  await expect(page.getByRole('link', { name: 'Security & access', exact: true })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Audit', exact: true })).toHaveCount(0);
  await page.selectOption('#theme-select', 'dark');
  await accentCard(page, 'rose').click();
  await page.selectOption('#density-select', 'compact');
  await expect(page.getByRole('status').filter({ hasText: 'Table spacing updated' })).toContainText('Saved for this browser');
  expect(mutations).toEqual([]);
});

test('native accent radios support keyboard selection, preserve semantic status and persist', async ({ page }) => {
  await page.goto(url('/settings'));
  await page.selectOption('#theme-select', 'dark');
  const status = page.locator('.appearance-statuses .ui-status-success');
  const color = await status.evaluate(el => getComputedStyle(el).color);
  const sage = page.getByRole('radio', { name: 'Sage', exact: true });
  await sage.focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Glacier', exact: true })).toBeChecked();
  await expect(page.getByRole('radio', { name: 'Glacier', exact: true })).toBeFocused();
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'glacier');
  await expect(status).toHaveCSS('color', color);
  await expect(accentCard(page, 'glacier')).toHaveCSS('outline-style', 'solid');
  await page.reload();
  await expect(page.getByRole('radio', { name: 'Glacier', exact: true })).toBeChecked();
  await expect(page.locator('#theme-select')).toHaveValue('dark');
  await page.goto(url('/stacks'));
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'glacier');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
});

test('density changes table padding, survives HTMX replacement, and synchronizes other tabs', async ({ page }) => {
  await page.goto(url('/settings'));
  const cell = page.locator('.appearance-table tbody td').first();
  const padding = parseFloat(await cell.evaluate(el => getComputedStyle(el).paddingTop));
  const font = await cell.evaluate(el => getComputedStyle(el).fontSize);
  const action = page.locator('.appearance-preview .ui-button-primary');
  const actionHeight = (await action.boundingBox()).height;
  await page.selectOption('#density-select', 'compact');
  const compact = await cell.evaluate(el => getComputedStyle(el).paddingTop);
  expect(parseFloat(compact)).toBeLessThan(padding);
  await expect(cell).toHaveCSS('font-size', font);
  expect((await action.boundingBox()).height).toBe(actionHeight);

  const tablePage = await page.context().newPage();
  await tablePage.goto(url('/containers'));
  await expect(tablePage.locator('#containers-body')).toContainText('grafana');
  const runtimeCell = tablePage.locator('#containers-body td').first();
  await expect(runtimeCell).toHaveCSS('padding-top', compact);
  await tablePage.evaluate(async () => {
    await htmx.ajax('GET', '/web/containers', { target: '#containers-body', swap: 'innerHTML' });
  });
  await expect(runtimeCell).toHaveCSS('padding-top', compact);
  await page.selectOption('#density-select', 'comfortable');
  await expect(tablePage.locator('html')).toHaveAttribute('data-density', 'comfortable');
  await expect(runtimeCell).toHaveCSS('padding-top', padding + 'px');
  await tablePage.close();
});

test('reset restores only appearance defaults and synchronizes open tabs', async ({ page }) => {
  await page.goto(url('/settings'));
  await page.evaluate(() => localStorage.setItem('unrelated-preference', 'keep-me'));
  await page.selectOption('#theme-select', 'light');
  await accentCard(page, 'rose').click();
  await page.selectOption('#density-select', 'compact');
  const other = await page.context().newPage();
  await other.goto(url('/settings'));
  await expect(other.getByRole('radio', { name: 'Rose', exact: true })).toBeChecked();
  await page.getByRole('button', { name: 'Reset preferences' }).click();
  for (const tab of [page, other]) {
    await expect(tab.locator('#theme-select')).toHaveValue('system');
    await expect(tab.getByRole('radio', { name: 'Sage', exact: true })).toBeChecked();
    await expect(tab.locator('#density-select')).toHaveValue('comfortable');
  }
  expect(await page.evaluate(() => ['theme', 'brine-accent', 'brine-density'].map(key => localStorage.getItem(key)))).toEqual([null, null, null]);
  expect(await page.evaluate(() => localStorage.getItem('unrelated-preference'))).toBe('keep-me');
  await page.reload();
  await expect(page.locator('#theme-select')).toHaveValue('system');
  await expect(page.getByRole('radio', { name: 'Sage', exact: true })).toBeChecked();
  await expect(page.locator('#density-select')).toHaveValue('comfortable');
  await other.close();
});

test('invalid stored or API preferences fall back safely', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('theme', 'invalid-theme');
    localStorage.setItem('brine-accent', 'invalid-accent');
    localStorage.setItem('brine-density', 'invalid-density');
  });
  await page.goto(url('/settings'));
  await expect(page.locator('#theme-select')).toHaveValue('system');
  await expect(page.getByRole('radio', { name: 'Sage', exact: true })).toBeChecked();
  await expect(page.locator('#density-select')).toHaveValue('comfortable');
  await page.evaluate(() => {
    setBrineTheme('invalid-theme'); setBrineAccent('invalid-accent'); setBrineDensity('invalid-density');
  });
  await expect(page.locator('#theme-select')).toHaveValue('system');
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'sage');
  await expect(page.locator('html')).toHaveAttribute('data-density', 'comfortable');
});

test('blocked storage keeps controls working in memory and reports the persistence limit', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await page.addInitScript(() => Object.defineProperty(window, 'localStorage', { get() { throw new Error('Storage blocked for test'); } }));
  await page.goto(url('/settings'));
  await expect(page.locator('#preference-storage-warning')).toBeVisible();
  await page.selectOption('#theme-select', 'dark');
  await accentCard(page, 'rose').click();
  await page.selectOption('#density-select', 'compact');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'rose');
  await expect(page.locator('html')).toHaveAttribute('data-density', 'compact');
  await expect(page.locator('#preference-feedback')).toContainText('this tab only');
  await page.reload();
  await expect(page.locator('#theme-select')).toHaveValue('system');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.getByRole('radio', { name: 'Sage', exact: true })).toBeChecked();
  await expect(page.locator('#density-select')).toHaveValue('comfortable');
});

test('storage write failures keep the new choice without falsely claiming it was saved', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.addInitScript(() => {
    Storage.prototype.setItem = () => { throw new DOMException('Quota exceeded for test', 'QuotaExceededError'); };
  });
  await page.goto(url('/settings'));
  await expect(page.locator('#preference-storage-warning')).toBeHidden();
  await page.selectOption('#theme-select', 'light');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.locator('#theme-select')).toHaveValue('light');
  await expect(page.locator('#preference-storage-warning')).toBeVisible();
  await expect(page.locator('#preference-feedback')).toContainText('Applied in this tab only');
  expect(await page.evaluate(() => localStorage.getItem('theme'))).toBeNull();
});

for (const theme of ['light', 'dark']) {
  test(`Settings stays readable and its gear reachable on narrow screens: ${theme}`, async ({ page }, testInfo) => {
    for (const width of [320, 768]) {
      await page.setViewportSize({ width, height: 844 });
      await page.goto(url('/settings'));
      await page.selectOption('#theme-select', theme);
      const settings = page.getByRole('link', { name: 'Settings', exact: true });
      await expect(settings).toBeVisible();
      const box = await settings.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual(844);
      if (width >= 768) {
        await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
        const pinned = await settings.boundingBox();
        expect(pinned.y).toBeGreaterThanOrEqual(0);
        expect(pinned.y + pinned.height).toBeLessThanOrEqual(844);
        await page.evaluate(() => window.scrollTo(0, 0));
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1);
      const badges = await page.locator('.appearance-table .ui-status').evaluateAll(elements => elements.map(el => {
        const css = getComputedStyle(el);
        return { height: el.getBoundingClientRect().height, singleLine: ['lineHeight', 'paddingTop', 'paddingBottom', 'borderTopWidth', 'borderBottomWidth'].reduce((sum, key) => sum + parseFloat(css[key]), 0) };
      }));
      for (const badge of badges) expect(badge.height).toBeLessThanOrEqual(badge.singleLine + 1);
      await page.screenshot({ path: testInfo.outputPath(`settings-${width}.png`), fullPage: true });
    }
  });
}
