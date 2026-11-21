const { test, expect } = require('@playwright/test');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

test('a disconnected deployment can be looked up again without resubmitting', async ({ page }) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'brine-deployment-status-'));
  try {
    execFileSync('go', ['test', './internal/remoteui', '-run', '^TestDeploymentStatusBrowserFixture$', '-count=1'], {
      env: { ...process.env, BRINE_DEPLOYMENT_STATUS_FIXTURE: dir },
    });
    const id = '1a23456789abcdef0123456789abcdef';
    const statusURL = `http://remote.test/remote/deployment?host=demo&id=${id}`;
    const requests = [];
    await page.route('http://remote.test/**', (route) => {
      const req = route.request();
      requests.push([req.method(), req.url()]);
      return route.fulfill({
        contentType: 'text/html',
        path: path.join(dir, requests.length === 1 ? 'unavailable.html' : 'unknown.html'),
      });
    });

    await page.setViewportSize({ width: 320, height: 640 });
    await page.goto(statusURL);
    await expect(page.getByRole('alert')).toContainText('Status unavailable');
    await expect(page.getByRole('alert')).toContainText('does not mean the deployment had no effect');
    await expect(page.getByRole('alert')).toContainText('Check the original target and its journal');
    await expect(page.getByRole('alert')).toContainText('do not retry based on this result');
    const requestID = page.getByRole('textbox', { name: 'Request ID' });
    await expect(requestID).toHaveValue(id);
    await expect(requestID).not.toBeEditable();
    await requestID.selectText();
    expect(await requestID.evaluate((input) => input.value.slice(input.selectionStart, input.selectionEnd))).toBe(id);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);

    await page.getByRole('link', { name: 'Refresh status' }).click();
    await expect(page.getByRole('alert')).toContainText('Outcome unknown. Dill recorded this request');
    await expect(page.getByRole('alert')).toContainText('do not retry');
    await expect(requestID).toHaveValue(id);
    expect(requests).toEqual([['GET', statusURL], ['GET', statusURL]]);

    await page.unroute('http://remote.test/**');
    await page.route('http://remote.test/**', (route) => {
      const req = route.request();
      requests.push([req.method(), req.url()]);
      return route.fulfill({ contentType: 'text/html', path: path.join(dir, 'partial.html') });
    });
    await page.goto(statusURL);
    await expect(page.getByRole('alert')).toContainText('The operation failed after partially changing the target.');
    await expect(page.getByRole('alert')).toContainText('Inspect the target and the recorded Dill operation');
    await expect(page.getByRole('alert')).toContainText('do not retry');
    await expect(requestID).toHaveValue(id);
    await expect(page.locator('main')).toContainText(`op_${id}`);
    await expect(page.locator('main')).toContainText('Target: demo');
    await expect(page.getByRole('link', { name: 'Refresh status' })).toHaveAttribute('href', `/remote/deployment?host=demo&id=${id}`);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
    await page.getByRole('link', { name: 'Refresh status' }).click();
    await expect(page.getByRole('alert')).toContainText('partially changing the target');
    expect(requests).toEqual(Array(4).fill(['GET', statusURL]));

    await page.unroute('http://remote.test/**');
    await page.route('http://remote.test/**', route => {
      const req = route.request();
      requests.push([req.method(), req.url()]);
      return route.fulfill({ contentType: 'text/html', path: path.join(dir, 'unverified.html') });
    });
    await page.goto(statusURL);
    await expect(page.getByRole('alert')).toContainText('Unverified status reply');
    await expect(page.getByRole('alert')).toContainText('cannot establish that the request was never admitted or had no effect');
    await expect(page.getByRole('alert')).toContainText('Check the original target and its journal');
    await expect(page.getByRole('alert')).toContainText('do not retry based on this result');
    await expect(requestID).toHaveValue(id);
    await expect(page.locator('main')).not.toContainText(/State not-admitted|Effects none|unverified-stack/);
    await expect(page.locator('form[method="post"]')).toHaveCount(0);
    await page.getByRole('link', { name: 'Refresh status' }).click();
    await expect(page.getByRole('alert')).toContainText('Unverified status reply');
    expect(requests).toEqual(Array(6).fill(['GET', statusURL]));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
