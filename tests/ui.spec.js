const { test, expect } = require('@playwright/test');
const path = require('node:path');
const fs = require('node:fs');

const helper = path.resolve(__dirname, '../internal/web/static/ui.js');

test.beforeEach(async ({ page }) => {
  await page.setContent('<main><div id="fields"></div></main>');
  await page.addScriptTag({ path: helper });
});

test('filenames remain text and cannot create HTML elements', async ({ page }) => {
  const filename = '"><img src=x onerror="window.injected=true">.pkl';
  await page.evaluate((name) => {
    const fields = BrineUI.createFileFields(document, name);
    document.querySelector('#fields').append(fields.input, fields.textarea);
  }, filename);
  await expect(page.locator('#fields input')).toHaveValue(filename);
  await expect(page.locator('#fields textarea')).toHaveAttribute('placeholder', '// ' + filename);
  await expect(page.locator('#fields img')).toHaveCount(0);
  expect(await page.evaluate(() => window.injected)).toBeUndefined();
});

test('stream status recognizes markers split at every position', async ({ page }) => {
  const results = await page.evaluate(() => {
    return ['[brine:done]', '[brine:error]'].flatMap(marker =>
      Array.from({ length: marker.length + 1 }, (_, i) => {
        const stream = BrineUI.createStreamStatus();
        stream.push(marker.slice(0, i));
        stream.push(marker.slice(i));
        return { marker, ...stream.status };
      }));
  });
  for (const result of results) {
    expect(result.done).toBe(result.marker === '[brine:done]');
    expect(result.error).toBe(result.marker === '[brine:error]');
  }
});


test('editor warns about unsaved changes and clears warning after save', async ({ page }) => {
  await page.setContent(`<form id="editor-form"><textarea id="pkl-editor">original</textarea></form>
    <span id="editor-dirty" class="hidden"></span><button id="fmt-btn"></button><div id="edit-result"></div>`);
  const template = fs.readFileSync(path.resolve(__dirname, '../internal/web/templates/stacks/editor.html'), 'utf8');
  const script = template.split('<script>\nconst snippetCache = {};')[1].split('</script>')[0];
  await page.addScriptTag({ content: 'const snippetCache = {};' + script });
  const dirty = page.locator('#editor-dirty');
  await page.locator('#pkl-editor').fill('modified');
  await expect(dirty).not.toHaveClass(/hidden/);
  expect(await page.evaluate(() => !window.dispatchEvent(new Event('beforeunload', { cancelable: true })))).toBe(true);
  await page.evaluate(() => {
    const form = document.querySelector('#editor-form');
    form.dispatchEvent(new CustomEvent('htmx:beforeRequest', { bubbles: true }));
    form.dispatchEvent(new CustomEvent('htmx:beforeOnLoad', { bubbles: true, detail: {
      xhr: { status: 200, getResponseHeader: () => '/stacks/example' }
    }}));
  });
  await expect(dirty).toHaveClass(/hidden/);
  expect(await page.evaluate(() => window.dispatchEvent(new Event('beforeunload', { cancelable: true })))).toBe(true);
});
