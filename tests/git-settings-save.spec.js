const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

for (const failure of ['network', 'HTTP']) {
  test(`Git settings preserve the draft and report ${failure} loss without a queued save`, async ({ page }) => {
    let writes = 0, started, release;
    const begun = new Promise(resolve => { started = resolve; });
    const held = new Promise(resolve => { release = resolve; });
    await page.route('**/web/stacks/media/git', async route => {
      expect(route.request().method()).toBe('PUT');
      expect(new URLSearchParams(route.request().postData()).get('git_branch')).toBe('kept-draft');
      if (++writes === 1) {
        started();
        await held;
        if (failure === 'network') return route.abort();
        return route.fulfill({ status: 502, contentType: 'text/html', body: '<b>Untrusted diagnostic</b>' });
      }
      return route.fulfill({ contentType: 'text/html', body: '<div>Git settings saved</div>' });
    });
    await page.goto(new URL('/stacks/media', base).href);
    const disclosure = page.locator('#git-source-panel summary');
    await disclosure.focus();
    await page.keyboard.press('Enter');
    await expect(disclosure.locator('..')).toHaveAttribute('open', '');
    const branch = page.getByRole('textbox', { name: 'Branch', exact: true });
    await branch.fill('kept-draft');
    const save = page.getByRole('button', { name: 'Save settings', exact: true });
    const result = page.getByRole('status').and(page.locator('#git-settings-result'));
    await save.focus();
    await page.keyboard.press('Enter');
    await begun;
    await expect(result).toHaveText('Saving Git settings…');
    await expect(save).toBeDisabled();
    await save.locator('xpath=ancestor::form').evaluate(form => form.requestSubmit());
    release();
    await expect(result).toContainText('Git settings save could not be confirmed');
    await expect(result).toContainText('Check the saved settings in another tab before retrying');
    await expect(result).not.toContainText('Untrusted diagnostic');
    await expect(page.locator('#git-result')).toBeEmpty();
    await expect(branch).toHaveValue('kept-draft');
    await expect(save).toBeEnabled();
    expect(writes).toBe(1);

    await save.click();
    await expect(result).toHaveText('Git settings saved');
    await expect(save).toBeEnabled();
    expect(writes).toBe(2);
  });
}
