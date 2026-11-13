const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Synthetic fixture only; no engine');

test('separate browser reconnect uses saved Brine ID and GET only, never automatic replay', async ({ browser }) => {
  const id = 'br_' + 'a'.repeat(32);
  let posts = 0, gets = 0;
  const first = await browser.newContext();
  const page = await first.newPage();
  await page.route('**/api/stacks/observability/stop', async route => {
    posts++;
    // Observer stream lost/missing terminal, but early request ID survives.
    await route.fulfill({status: 200, headers: {'Content-Type': 'text/plain', 'X-Brine-Request-ID': id}, body: ''});
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.locator('#action-output').evaluate(el => { el.dataset.qualifiedOperations = 'true'; });
  await page.evaluate(() => runAction('stop', 'observability'));
  await expect(page.locator('#operation-request-id')).toHaveValue(id);
  await expect(page.locator('#operation-lookup')).toContainText('Save this ID');
  await first.close();

  const second = await browser.newContext();
  const reconnect = await second.newPage();
  await reconnect.route('**/api/operations/**', async route => {
    gets++;
    expect(route.request().method()).toBe('GET');
    expect(route.request().url().endsWith('/' + id)).toBeTruthy();
    await route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({
      version: 'brine.operation/1', requestId: id, phase: 'terminal', status: 'failed', persistence: 'durable', delivery: 'write_failed', reason: 'terminal_observed', sourceBinding: 'UNBOUND',
      dillOperationId: 'op_' + 'b'.repeat(32), invocation: {exitCode: 1, status: 'failed', receipt: {outcome: 'succeeded', effects: 'complete'}},
    })});
  });
  await reconnect.route('**/api/stacks/**/stop', async route => { posts++; await route.abort(); });
  await reconnect.goto(new URL('/stacks/observability', baseURL).href);
  await reconnect.evaluate(() => BrineUI.operationLookup(document, document.getElementById('action-output').parentNode));
  await reconnect.locator('#operation-request-id').fill(id);
  expect(gets).toBe(0); // No polling, even after a new browser loads.
  await reconnect.getByRole('button', {name: 'Look up status', exact: true}).click();
  await expect(reconnect.locator('#operation-lookup')).toContainText('Outcome: succeeded');
  await expect(reconnect.locator('#operation-lookup')).toContainText('Effects: complete');
  await expect(reconnect.locator('#operation-lookup')).toContainText('Invocation status: failed');
  await expect(reconnect.locator('#operation-lookup')).toContainText('Process exit: 1');
  await expect(reconnect.locator('#operation-lookup')).toContainText('no browser acknowledgement');
  await reconnect.locator('#operation-request-id').fill('../private-state');
  await reconnect.getByRole('button', {name: 'Look up status', exact: true}).click();
  await expect(reconnect.locator('#operation-lookup')).toContainText('Enter an exact Brine request ID');
  expect(gets).toBe(1);
  expect(posts).toBe(1);
  await second.close();
});
