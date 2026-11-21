const { test, expect } = require('@playwright/test');

const base = process.env.BRINE_FIXTURE_BASE_URL;
const stack = 'observability';

async function activate(page, action) {
  const labels = { up: 'Deploy', down: 'Down', stop: 'Stop', start: 'Start', restart: 'Restart', teardown: 'Teardown' };
  if (action === 'teardown') page.once('dialog', dialog => dialog.accept(stack));
  const button = page.getByRole('button', { name: labels[action], exact: true });
  await button.focus();
  await page.keyboard.press('Enter');
  if (action === 'down') await page.getByRole('dialog', { name: 'Run down' }).getByRole('button', { name: 'Confirm' }).click();
  return button;
}

for (const action of ['up', 'down', 'stop', 'start', 'restart', 'teardown']) {
  test(`an unterminated ${action} stream leaves the local outcome unknown`, async ({ page }) => {
    let posts = 0;
    await page.route(`**/api/stacks/${stack}/${action}`, route => {
      expect(route.request().method()).toBe('POST');
      posts++;
      return route.fulfill({ contentType: 'text/plain', body: 'Synthetic progress without a terminal result\n' });
    });
    await page.goto(new URL(`/stacks/${stack}`, base).href);
    const button = await activate(page, action);
    const status = page.locator('#action-status');
    await expect(status).toHaveAttribute('role', 'status');
    await expect(status).toHaveText('unknown');
    await expect(page.locator('#action-log')).toContainText('Inspect resources before retrying.');
    await expect(button).toBeEnabled();
    expect(posts).toBe(1);
  });
}

for (const outcome of ['network error', 'read error', 'observed error marker', 'error marker then read error']) {
  test(`local Down distinguishes ${outcome} from confirmed completion`, async ({ page }) => {
    let posts = 0;
    const marker = '[brine:error] down failed: synthetic command error\n';
    const observed = outcome.includes('error marker');
    const endpoint = `/api/stacks/${stack}/down`;
    await page.route(`**${endpoint}`, route => {
      posts++;
      if (outcome === 'network error') return route.abort();
      return route.fulfill({ contentType: 'text/plain', body: observed ? marker : 'synthetic progress\n' });
    });
    if (outcome.includes('read error')) {
      await page.addInitScript(({ endpoint, marker, observed }) => {
        const realFetch = window.fetch;
        window.fetch = async (url, init) => {
          const response = await realFetch(url, init);
          if (url !== endpoint) return response;
          const reader = response.body.getReader();
          let first = true;
          return { ok: response.ok, body: { getReader: () => ({
            async read() {
              if (!first) throw new TypeError('Synthetic body interruption');
              first = false;
              await reader.cancel();
              return { done: false, value: new TextEncoder().encode(observed ? marker : 'Synthetic progress\n') };
            },
          }) } };
        };
      }, { endpoint, marker, observed });
    }
    await page.goto(new URL(`/stacks/${stack}`, base).href);
    const button = await activate(page, 'down');
    await expect(page.locator('#action-status')).toHaveText(observed ? 'failed' : 'unknown');
    if (!observed) await expect(page.locator('#action-log')).toContainText('Inspect resources before retrying.');
    await expect(button).toBeEnabled();
    expect(posts).toBe(1);
  });
}
