const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

for (const source of ['local', 'git']) {
  for (const failure of ['network', 'http']) {
    test(`${source} creation reports unconfirmed ${failure} outcome`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width: 390, height: 850 });
      let requests = 0;
      await page.route(source === 'local' ? '**/web/stacks' : '**/web/stacks/git', route => {
        requests++;
        return failure === 'network' ? route.abort() : route.fulfill({ status: 503 });
      });
      await page.goto(new URL('/stacks/new', base).href);
      if (source === 'git') {
        await page.getByRole('button', { name: 'Git', exact: true }).click();
        await page.getByLabel('Git URL').fill('https://example.invalid/project.git');
      }
      await page.locator(source === 'local' ? '#local-stack-name' : '#git-stack-name').fill('fixture-stack');
      const submit = page.getByRole('button', { name: source === 'local' ? 'Create stack' : 'Clone & create' });
      await submit.focus(); await page.keyboard.press('Enter');
      const result = page.locator(source === 'local' ? '#local-result' : '#git-result');
      await expect(result).toContainText('Creation could not be confirmed');
      await expect(result).toContainText('partial files before retrying');
      await expect(result).toHaveAttribute('role', 'status');
      expect(requests).toBe(1);
      await page.screenshot({ path: testInfo.outputPath('creation-uncertain.png') });
    });
  }
}

test('source selection is accessible and in-flight creation blocks both submits', async ({ page }) => {
  let release, started, requests = 0;
  const held = new Promise(resolve => { release = resolve; });
  const begun = new Promise(resolve => { started = resolve; });
  await page.route('**/web/stacks', async route => { requests++; started(); await held; await route.fulfill({ status: 200, body: 'Synthetic validation result' }); });
  await page.goto(new URL('/stacks/new', base).href);
  const local = page.getByRole('button', { name: 'Local', exact: true });
  const git = page.getByRole('button', { name: 'Git', exact: true });
  await expect(local).toHaveAttribute('aria-pressed', 'true');
  await page.locator('#local-stack-name').fill('fixture-stack');
  await page.getByRole('button', { name: 'Create stack' }).click(); await begun;
  await expect(page.locator('#local-result')).toContainText('Creating stack');
  await git.focus(); await page.keyboard.press('Enter');
  await expect(git).toHaveAttribute('aria-pressed', 'true');
  await expect(local).toHaveAttribute('aria-pressed', 'false');
  await expect(page.getByRole('button', { name: 'Clone & create' })).toBeDisabled();
  release();
  await expect(page.getByRole('button', { name: 'Clone & create' })).toBeEnabled();
  expect(requests).toBe(1);
  await local.click();
  await expect(page.getByRole('button', { name: 'Add file' })).toBeVisible();
});
