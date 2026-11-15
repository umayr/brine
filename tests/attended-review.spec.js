const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Synthetic read-only fixture only; no engine');

test('synthetic attended review displays every row and requires a human confirmation', async ({ page }) => {
  const id = 'br_' + 'a'.repeat(32), applyID = 'br_' + 'b'.repeat(32);
  const rows = Array.from({length: 255}, (_, i) => ({kind: 'container', name: 'synthetic_service_' + String(i).padStart(3, '0'), identity: 'synthetic_service_' + String(i).padStart(3, '0'), identityKind: 'planned-name', presence: 'absent', action: 'create', retention: 'not-applicable'}));
  rows.push({kind:'network', name:'synthetic', identity:'synthetic', identityKind:'planned-name', presence:'absent', action:'ensure', retention:'preserve'});
  let previews = 0, applies = 0, lookups = 0;
  await page.route('**/api/stacks/observability/operations/preview', async route => {
    previews++;
    await route.fulfill({status: 200, headers: {'Content-Type': 'application/json', 'X-Brine-Request-ID': id}, body: JSON.stringify({requestId: id, reviewDigest: 'digest', confirmationToken: 'synthetic-token', review: {version: 'brine.operation/2', intent: {source: {snapshotDigest: 'assertion'}}, preview: {result: {version:'dill.preflight/1',operationId:'op_'+'0'.repeat(31)+'1',operation:'up',target:{engine:'docker',endpointDigest:'sha256:'+'1'.repeat(64),qualifiedIdentityDigest:'sha256:'+'2'.repeat(64)},stack:'synthetic',outcome:'succeeded',engineEffects:'none',coordinationEffects:'written',inputDigest:'sha256:'+'7'.repeat(64),selectionDigest:'sha256:'+'8'.repeat(64),coverage:{containers:{scope:'declared-stack',count:255,presentCount:0,complete:true},network:{scope:'stack-network',count:1,presentCount:0,complete:true},volumes:{scope:'declared-attached-named',count:0,presentCount:0,complete:true}},resources:rows,resourcesTruncated:false,errors:[]}}}})});
  });
  await page.route('**/api/stacks/observability/up', async route => {
    applies++;
    await route.fulfill({status: 200, headers: {'X-Brine-Request-ID': applyID, 'Content-Type': 'text/plain'}, body: ''});
  });
  await page.route('**/api/operations/**', async route => {
    lookups++;
    await route.fulfill({status: 200, contentType: 'application/json', body: JSON.stringify({version: 'brine.operation/2', requestId: applyID, kind: 'apply', phase: 'terminal', persistence: 'durable', delivery: 'write_failed', apply: {status: 'failed', exitCode: 1, receipt: {outcome:'failed',effects:'none',errors:[{code:'PREFLIGHT_MISMATCH',message:'Reviewed resource selection changed. Review again before retrying.'}]}}})});
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await page.addScriptTag({url: new URL('/static/attended.js', baseURL).href});
  await page.evaluate(() => { document.getElementById('action-output').dataset.attended = 'true'; });
  await page.evaluate(() => runAction('up', 'observability'));
  const log = page.locator('#action-log');
  await expect(log).toContainText('synthetic_service_254');
  await expect(log).toContainText('"count": 255');
  await expect(log).toContainText('not a backup');
  await expect(log.locator('literal')).toHaveCount(0);
  expect(previews).toBe(1); expect(applies).toBe(0); expect(lookups).toBe(0);
  await page.getByRole('button', {name: 'Confirm once'}).click();
  expect(applies).toBe(0);
  await page.locator('#attended-confirm input').check();
  await page.getByRole('button', {name: 'Confirm once'}).click();
  await expect.poll(() => applies).toBe(1);
  await expect(log).toContainText('not acknowledgement or proof of success');
  expect(lookups).toBe(0);
  await page.getByRole('button', {name: 'Look up status', exact: true}).click();
  await expect(page.locator('#operation-lookup')).toContainText('write_failed');
  await expect(page.locator('#operation-lookup')).toContainText('PREFLIGHT_MISMATCH');
  expect(lookups).toBe(1); expect(applies).toBe(1);
});
