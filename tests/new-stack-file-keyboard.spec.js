const { test, expect } = require('@playwright/test');
const { focusNext } = require('./focus-traversal');

const base = process.env.BRINE_FIXTURE_BASE_URL;

test('new stack files can be added, selected and removed with the keyboard', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 320, height: 850 });
  await page.goto(new URL('/stacks/new', base).href);
  const files = page.getByRole('group', { name: 'Stack files' });
  const compose = files.getByRole('button', { name: 'compose.pkl', exact: true });
  const add = page.getByRole('button', { name: 'Add file', exact: true });
  const filename = page.getByRole('textbox', { name: 'New filename' });
  const keyboard = page.keyboard;

  await expect(compose).toHaveAttribute('aria-pressed', 'true');
  await add.focus();
  await keyboard.press('Enter');
  await expect(filename).toBeFocused();
  await expect(add).toHaveAttribute('aria-expanded', 'true');
  await keyboard.press('Escape');
  await expect(add).toBeFocused();
  await expect(add).toHaveAttribute('aria-expanded', 'false');
  await expect(filename).toBeHidden();

  await keyboard.press('Enter');
  await keyboard.type('keep');
  await keyboard.press('Enter');
  const keep = files.getByRole('button', { name: 'keep.pkl', exact: true });
  await expect(keep).toBeFocused();
  await expect(keep).toHaveAttribute('aria-pressed', 'true');
  await expect(compose).toHaveAttribute('aria-pressed', 'false');
  const keepEditor = page.getByRole('textbox', { name: 'Contents of keep.pkl' });
  await expect(keepEditor).toBeVisible();
  await keepEditor.fill('// Keep this file');

  await add.focus();
  await keyboard.press('Enter');
  await keyboard.type('extra');
  await keyboard.press('Enter');
  const extra = files.getByRole('button', { name: 'extra.pkl', exact: true });
  await expect(extra).toBeFocused();
  await expect(extra).toHaveAttribute('aria-pressed', 'true');
  await focusNext(page, testInfo);
  const remove = files.getByRole('button', { name: 'Remove extra.pkl', exact: true });
  await expect(remove).toBeFocused();
  await keyboard.press('Space');
  await expect(extra).toHaveCount(0);
  await expect(remove).toHaveCount(0);
  await expect(compose).toBeFocused();
  await expect(compose).toHaveAttribute('aria-pressed', 'true');
  await expect(files.getByRole('button', { name: 'Remove compose.pkl' })).toHaveCount(0);

  await focusNext(page, testInfo);
  await expect(keep).toBeFocused();
  await keyboard.press('Enter');
  await expect(keep).toHaveAttribute('aria-pressed', 'true');
  await expect(keepEditor).toHaveText('// Keep this file');
  const submittedFiles = await page.locator('#form-local form').evaluate(form => {
    const data = new FormData(form);
    return [data.getAll('filename'), data.getAll('filecontent').at(-1)];
  });
  expect(submittedFiles).toEqual([['compose.pkl', 'keep.pkl'], '// Keep this file']);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
});
