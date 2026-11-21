const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

for (const [outcome, message] of [
  ['engine error', 'The engine reported an image pull error.'],
  ['completed progress', 'Engine finished reporting pull progress without an error. Refresh image inventory to verify the image.'],
  ['network failure', 'Image pull could not be confirmed.'],
  ['HTTP failure', 'Image pull could not be confirmed.'],
]) {
  test(`image pull dialog reports ${outcome} inline without a duplicate request`, async ({ page }) => {
    let requests = 0;
    let release;
    const pending = new Promise(resolve => { release = resolve; });
    await page.route('**/web/images/pull', async route => {
      requests++;
      expect(route.request().method()).toBe('POST');
      expect(route.request().postData()).toBe('ref=example%3Alocal');
      await pending;
      if (outcome === 'network failure') return route.abort();
      if (outcome === 'HTTP failure') return route.fulfill({ status: 504, contentType: 'text/html', body: '<strong>Unexpected error response</strong>' });
      return route.fulfill({ status: 200, contentType: 'text/html', body: `<div>${message}</div>` });
    });
    await page.goto(new URL('/images', base).href);
    await page.getByRole('button', { name: 'Pull image', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Pull image' });
    const ref = dialog.getByRole('textbox', { name: 'Image reference' });
    const submit = dialog.getByRole('button', { name: 'Pull', exact: true });
    await ref.fill('example:local');
    await submit.click();
    const status = dialog.getByRole('status');
    await expect(status).toHaveText('Pulling image…');
    await expect(submit).toBeDisabled();
    await expect(ref).toBeDisabled();
    await dialog.locator('form[hx-post]').evaluate(form => form.requestSubmit());
    release();
    await expect(status).toContainText(message);
    await expect(status).not.toContainText(/event: done|pull complete|Unexpected error response/i);
    await expect(ref).toHaveValue('example:local');
    await expect(submit).toBeEnabled();
    expect(requests).toBe(1);
  });
}

test('a timed-out pull releases the dialog without resubmitting', async ({ page }) => {
  let requests = 0;
  let release;
  const hold = new Promise(resolve => { release = resolve; });
  await page.route('**/web/images/pull', async route => {
    requests++;
    await hold;
    await route.abort().catch(() => {});
  });
  await page.goto(new URL('/images', base).href);
  await page.getByRole('button', { name: 'Pull image', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Pull image' });
  const form = dialog.locator('form[hx-post]');
  await expect(form).toHaveAttribute('hx-request', '{"timeout":60000}');
  await form.evaluate(el => el.setAttribute('hx-request', '{"timeout":500}'));
  const ref = dialog.getByRole('textbox', { name: 'Image reference' });
  const submit = dialog.getByRole('button', { name: 'Pull', exact: true });
  await ref.fill('example:local');
  await submit.click();
  try {
    await expect(dialog.getByRole('status')).toContainText('Image pull could not be confirmed');
    await expect(submit).toBeEnabled();
    await expect(ref).toBeEnabled();
    expect(requests).toBe(1);
  } finally {
    release();
  }
});
