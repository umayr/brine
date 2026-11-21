const { test, expect } = require('@playwright/test');

const base = process.env.BRINE_FIXTURE_BASE_URL;
const editPath = '/recipes/simple-web-service/edit';

for (const [mode, routePath, endpoint, method, action] of [
  ['create', '/recipes/new', '**/web/recipes', 'POST', 'Create snippet'],
  ['edit', editPath, '**/web/recipes/simple-web-service', 'PUT', 'Save'],
]) {
  test(`${mode} keeps an unconfirmed save visible and drops duplicate submits`, async ({ page }) => {
    let requests = 0;
    let release;
    const pending = new Promise(resolve => { release = resolve; });
    const networkFailure = mode === 'create';
    await page.route(endpoint, async route => {
      expect(route.request().method()).toBe(method);
      if (++requests === 1) {
        await pending;
        if (networkFailure) return route.abort();
        return route.fulfill({ status: 503, contentType: 'text/html', body: '<h1>Unexpected response text</h1>' });
      }
      return networkFailure
        ? route.fulfill({ status: 200, headers: { 'HX-Redirect': editPath } })
        : route.fulfill({ status: 200, contentType: 'text/html', body: '<div>Saved</div>' });
    });
    await page.goto(new URL(routePath, base).href);
    const name = page.getByRole('textbox', { name: 'Name', exact: true });
    await name.fill('My snippet');
    if (mode === 'create') await page.getByRole('textbox', { name: 'Pkl body' }).fill('// Draft');
    const button = page.getByRole('button', { name: action, exact: true });
    const result = page.getByRole('status').and(page.locator('#recipe-result'));
    await button.click();
    await expect(result).toHaveText('Saving snippet…');
    await expect(button).toBeDisabled();
    if (mode === 'edit') await expect(page.getByRole('button', { name: 'Delete', exact: true })).toBeDisabled();
    await page.locator('#recipe-form').evaluate(form => form.requestSubmit());
    release();
    await expect(result).toContainText('Snippet save could not be confirmed');
    await expect(result).not.toContainText('Unexpected response text');
    await expect(name).toHaveValue('My snippet');
    await expect(button).toBeEnabled();
    expect(requests).toBe(1);

    await button.focus();
    await page.keyboard.press('Enter');
    if (mode === 'create') await expect(page).toHaveURL(new URL(editPath, base).href);
    else await expect(result).toHaveText('Saved');
    expect(requests).toBe(2);
  });
}
