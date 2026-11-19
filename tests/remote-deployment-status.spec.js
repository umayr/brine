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
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
