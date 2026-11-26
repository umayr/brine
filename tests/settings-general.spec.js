const { test, expect } = require('@playwright/test');

test.skip(!process.env.BRINE_CREDENTIAL_FIXTURE_DIR, 'Requires a disposable production credential fixture');
const root = 'http://127.0.0.1:18085';
const original = 'fixture-only-not-a-secret';
const replacement = 'fixture-updated-password';
const originalToken = 'fixture-only-token';

async function signIn(page, user, password) {
  await page.goto(root + '/login?next=%2Fsettings%2Fgeneral');
  await page.getByLabel('Username').fill(user);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL('**/settings/general');
}

test('General uses one password confirmation, rotates tokens, and revokes old browser sessions on sign-in changes', async ({ browser, request }) => {
  const first = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const second = await browser.newContext();
  const page = await first.newPage();
  const other = await second.newPage();
  const errors = [];
  page.on('pageerror', err => errors.push(err.message));
  const apiStatus = async (token) => (await request.get(root + '/api/stacks', {
    headers: { Authorization: 'Bearer ' + token },
  })).status();

  await signIn(page, 'admin', original);
  await signIn(other, 'admin', original);
  const settings = page.getByRole('navigation', { name: 'Settings sections' });
  await settings.getByRole('link', { name: 'Appearance' }).click();
  await settings.getByRole('link', { name: 'General' }).click();
  await expect(page.getByRole('heading', { name: 'General' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(391);
  const api = page.getByRole('region', { name: 'API access' });
  const signin = page.getByRole('region', { name: 'Sign-in' });
  const dialog = page.getByRole('dialog');
  const password = dialog.getByLabel('Current password');
  await expect(page.getByLabel('Current password')).toBeHidden();
  expect((await page.content()).includes(originalToken)).toBe(false);
  const reveal = { action: 'show', current_password: original };
  const noCSRF = await first.request.post(root + '/settings/general', { data: reveal });
  expect(noCSRF.status()).toBe(403);
  const bearer = await request.post(root + '/settings/general', {
    headers: { Authorization: 'Bearer ' + originalToken }, data: reveal,
  });
  expect(bearer.status()).toBe(403);

  await api.getByRole('button', { name: 'Show', exact: true }).click();
  await expect(password).toBeFocused();
  await password.fill('incorrect-fixture-password');
  await dialog.getByRole('button', { name: 'Show', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('current password is incorrect');
  await password.fill(original);
  const [revealed] = await Promise.all([
    page.waitForResponse(response => response.url() === root + '/settings/general' && response.request().method() === 'POST'),
    dialog.getByRole('button', { name: 'Show', exact: true }).click(),
  ]);
  expect(revealed.headers()['cache-control']).toBe('no-store');
  await expect(api.getByText(originalToken)).toBeVisible();
  await api.getByRole('button', { name: 'Hide', exact: true }).click();
  await expect(api.getByText(originalToken)).toHaveCount(0);

  await api.getByRole('button', { name: 'Regenerate', exact: true }).click();
  await expect(dialog.getByText('The old token stops working immediately.', { exact: false })).toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  expect(await apiStatus(originalToken)).toBe(200);
  await api.getByRole('button', { name: 'Regenerate', exact: true }).click();
  await password.fill(original);
  await dialog.getByRole('button', { name: 'Regenerate', exact: true }).click();
  await expect(api.getByRole('status')).toContainText('Token regenerated');
  const token = await api.locator('code').textContent();
  expect(await apiStatus(originalToken)).toBe(401);
  expect(await apiStatus(token)).toBe(200);
  await other.reload();
  await expect(other.getByRole('heading', { name: 'General' })).toBeVisible();

  await signin.getByLabel('Username').fill('updated-admin');
  await signin.getByLabel('New password', { exact: true }).fill(replacement);
  await signin.getByLabel('Confirm new password').fill(replacement);
  await signin.getByRole('button', { name: 'Update', exact: true }).click();
  await expect(dialog).toHaveAccessibleName('Update sign-in?');
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(signin.getByLabel('Username')).toHaveValue('updated-admin');
  await expect(signin.getByLabel('New password', { exact: true })).toHaveValue(replacement);
  await signin.getByRole('button', { name: 'Update', exact: true }).click();
  await password.fill(original);
  await dialog.getByRole('button', { name: 'Update', exact: true }).click();
  await expect(page).toHaveURL(/\/login\?/);
  await other.reload();
  await expect(other).toHaveURL(/\/login\?/);
  expect(await apiStatus(token)).toBe(200);
  await signIn(page, 'updated-admin', replacement);
  await expect(signin.getByLabel('Username')).toHaveValue('updated-admin');
  expect(errors).toEqual([]);
  await first.close();
  await second.close();
});
