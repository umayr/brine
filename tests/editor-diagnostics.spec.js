const { test, expect } = require('@playwright/test');

test('editor keeps in-flight diagnostics and formatting tied to the submitted draft', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const editor = page.locator('.cm-content');
  await expect(editor).toBeVisible();
  let release;
  await page.route('**/web/stacks/observability/validate', async route => {
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({status: 200, contentType: 'text/html', body: '<div>Draft validated</div>'});
  });
  await page.locator('#validate-btn').click();
  await expect(page.locator('#edit-result')).toHaveText('Validating draft…');
  await expect(page.locator('#validate-btn')).toBeDisabled();
  await editor.click(); await page.keyboard.type('new');
  release();
  await expect(page.locator('#edit-result')).toContainText('changed during validation');
  await page.route('**/web/stacks/observability/fmt', async route => {
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({status: 200, contentType: 'text/plain', body: 'formatted'});
  });
  release = null;
  await page.locator('#fmt-btn').click();
  await expect(page.locator('#edit-result')).toHaveText('Formatting draft…');
  await editor.click(); await page.keyboard.type('later');
  release();
  await expect(page.locator('#edit-result')).toContainText('not applied');
  await expect(editor).toContainText('later');
});

test('successful format is source data, marks draft dirty, and diagnostics become stale on edit', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const source = '<div id="edit-result" hx-swap-oob="innerHTML"><img src=x onerror="window.injected=true"></div>';
  await page.route('**/web/stacks/observability/fmt', route => route.fulfill({ status: 200, contentType: 'text/plain', body: source }));
  await page.locator('#fmt-btn').click();
  await expect(page.locator('.cm-content')).toHaveText(source);
  await expect(page.locator('#editor-dirty')).toBeVisible();
  await expect(page.locator('#edit-result')).toContainText('Formatting applied');
  await expect(page.locator('#edit-result img')).toHaveCount(0);
  expect(await page.evaluate(() => window.injected)).toBeUndefined();
  await page.route('**/web/stacks/observability/validate', route => route.fulfill({ status: 200, contentType: 'text/html', body: '<div>Draft validated</div>' }));
  await page.locator('#validate-btn').click();
  await expect(page.locator('#edit-result')).toContainText('Draft validated');
  await page.locator('.cm-content').click(); await page.keyboard.type('x');
  await expect(page.locator('#edit-result')).toContainText('changed since the last check');
});

test('format errors are plain text and do not replace the draft', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const editor = page.locator('.cm-content');
  const original = await editor.innerText();
  await page.route('**/web/stacks/observability/fmt', route => route.fulfill({ status: 422, contentType: 'text/plain', body: '<img src=x onerror="window.injected=true">\nline 2' }));
  await page.locator('#fmt-btn').click();
  await expect(page.locator('#edit-result')).toContainText('<img src=x');
  await expect(page.locator('#edit-result')).toContainText('line 2');
  await expect(page.locator('#edit-result img')).toHaveCount(0);
  expect(await editor.innerText()).toBe(original);
  expect(await page.evaluate(() => window.injected)).toBeUndefined();
});

test('save preserves newer edits and serializes editor operations', async ({ page }) => {
  await page.goto(new URL('/stacks/observability/edit', process.env.BRINE_FIXTURE_BASE_URL).href);
  const editor = page.locator('.cm-content');
  await editor.click(); await page.keyboard.type('first');
  let release, saves = 0, validates = 0;
  await page.route('**/web/stacks/observability/validate', route => { validates++; return route.fulfill({status: 200, body: 'unexpected'}); });
  await page.route('**/web/stacks/observability', async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    saves++;
    if (saves === 1) await new Promise(resolve => { release = resolve; });
    await route.fulfill({status: 200, headers: {'HX-Redirect': '/stacks/observability'}, body: ''});
  });
  await page.locator('#editor-form button[type=submit]').click();
  await expect.poll(() => saves).toBe(1);
  await page.locator('#validate-btn').click();
  await page.keyboard.press('Control+s');
  await editor.click(); await page.keyboard.type('second');
  expect(validates).toBe(0); expect(saves).toBe(1);
  release();
  await expect(page.locator('#edit-result')).toContainText('Earlier content saved');
  await expect(page.locator('#editor-dirty')).toBeVisible();
  await page.locator('#editor-form button[type=submit]').click();
  await expect.poll(() => saves).toBe(2);
  await expect(page).toHaveURL(/\/stacks\/observability$/);
});
