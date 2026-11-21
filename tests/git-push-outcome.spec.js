const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;
const path = '/api/stacks/media/git/push';

for (const [outcome, expected] of [
  ['unterminated', 'unknown'], ['network loss', 'unknown'], ['body read failure', 'unknown'],
  ['error marker', 'failed'], ['done marker', 'complete'],
  ['error marker then read failure', 'failed'], ['done marker then read failure', 'complete'],
]) {
  test(`Git push reports ${outcome} as ${expected} without replay`, async ({ page }) => {
    let posts = 0;
    await page.route(`**${path}`, route => {
      expect(route.request().method()).toBe('POST');
      expect(route.request().postDataJSON()).toEqual({ message: 'Synthetic commit' });
      posts++;
      if (outcome === 'network loss') return route.abort();
      const body = outcome.startsWith('error marker') ? '[brine:error] git push failed: synthetic\n' :
        outcome.startsWith('done marker') ? '[brine:done] git push complete\n' : 'Synthetic progress without a terminal marker\n';
      return route.fulfill({ contentType: 'text/plain', body });
    });
    if (outcome.endsWith('read failure')) {
      await page.addInitScript(path => {
        const fetch = window.fetch;
        window.fetch = async (url, init) => {
          const response = await fetch(url, init);
          if (url !== path) return response;
          let first = true;
          return { ok: response.ok, body: { getReader: () => ({
            async read() {
              if (!first) throw new TypeError('Synthetic body interruption');
              first = false;
              const body = await response.text();
              return { done: false, value: new TextEncoder().encode(body) };
            },
          }) } };
        };
      }, path);
    }
    await page.goto(new URL('/stacks/media', base).href);
    const opener = page.getByRole('button', { name: 'Push staged changes', exact: true });
    await opener.focus();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: 'Push changes' });
    await dialog.getByRole('textbox', { name: 'Commit message' }).fill('Synthetic commit');
    await dialog.getByRole('button', { name: 'Push', exact: true }).click();
    const status = page.locator('#action-status');
    await expect(status).toHaveAttribute('role', 'status');
    await expect(status).toHaveText(expected);
    if (expected === 'unknown') {
      await expect(page.locator('#action-title')).toHaveText('Git push outcome unknown');
      await expect(page.locator('#action-log')).toContainText('Inspect the local repository and remote branch before retrying.');
    } else {
      await expect(page.locator('#action-title')).not.toContainText('outcome unknown');
    }
    await expect(page.getByRole('button', { name: 'Deploy', exact: true })).toBeEnabled();
    expect(posts).toBe(1);
  });
}
