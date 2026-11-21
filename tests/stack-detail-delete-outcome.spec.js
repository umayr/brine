const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;
const resource = '/stacks/observability';

for (const scenario of ['network loss', 'HTTP failure', 'unexpected HTML success', 'confirmed deletion']) {
  test(`stack detail Delete reports ${scenario} without replay`, async ({ page }) => {
    let posts = 0, started, release;
    const begun = new Promise(resolve => { started = resolve; });
    const held = new Promise(resolve => { release = resolve; });
    const errors = [];
    page.on('pageerror', err => errors.push(err.message));
    await page.route(`**/api${resource}`, async route => {
      expect(route.request().method()).toBe('DELETE');
      expect(route.request().postDataJSON()).toEqual({ remove_containers: true });
      posts++;
      started();
      await held;
      if (scenario === 'network loss') return route.abort();
      if (scenario === 'HTTP failure') return route.fulfill({ status: 502, body: 'Synthetic diagnostic' });
      if (scenario === 'unexpected HTML success') return route.fulfill({ status: 200, contentType: 'text/html', body: '<div>Unexpected HTML</div>' });
      return route.fulfill({ status: 204 });
    });
    await page.goto(new URL(resource, base).href);
    const remove = page.getByRole('button', { name: 'Delete', exact: true });
    const deploy = page.getByRole('button', { name: 'Deploy', exact: true });
    await remove.focus();
    page.once('dialog', dialog => dialog.accept('not-the-stack'));
    await page.keyboard.press('Enter');
    await expect(page.getByText('Confirmation did not match the stack name')).toBeVisible();
    expect(posts).toBe(0);
    page.once('dialog', dialog => dialog.accept('observability'));
    await page.keyboard.press('Enter');
    await begun;
    const status = page.getByRole('status').and(page.locator('#stack-delete-result'));
    await expect(status).toHaveText('Deleting stack…');
    await expect(remove).toBeDisabled();
    await expect(deploy).toBeDisabled();
    await remove.evaluate(el => el.click());
    release();
    if (scenario === 'confirmed deletion') {
      await expect(page).toHaveURL(new URL('/stacks', base).href);
    } else {
      await expect(status).toContainText('Delete could not be confirmed');
      await expect(status).toContainText('Check stack inventory and inspect containers before retrying');
      await expect(status).not.toContainText(/Synthetic diagnostic|Unexpected HTML/);
      await expect(page).toHaveURL(new URL(resource, base).href);
      await expect(remove).toBeEnabled();
      await expect(deploy).toBeEnabled();
      await expect(remove).toBeFocused();
    }
    expect(posts).toBe(1);
    expect(errors).toEqual([]);
  });
}
