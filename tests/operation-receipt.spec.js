const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;

test('read-only marker coherence rejects engine observations but preserves planned close-failure observations', async ({ page }) => {
  await page.goto(new URL('/stacks/observability', baseURL).href);
  const results = await page.evaluate(() => {
    const results = [];
    for (const operation of ['plan', 'diff', 'validate']) {
      const receipt = { version:'dill.operation/1',operationId:'op_'+'e'.repeat(32),operation,stack:'observability',
        target:{engine:'docker',endpointDigest:'sha256:'+'b'.repeat(64)},outcome:'succeeded',effects:'none',changesDetected:false,
        resources:[{kind:'container',identityDigest:'sha256:'+'f'.repeat(64),action:'stop',effect:'complete'}],resourcesTruncated:false,errors:[] };
      function parse(invocation) {
        const stream = BrineUI.createStreamStatus({qualified:true,operation,stack:'observability'});
        stream.push('[brine:operation] '+JSON.stringify(invocation)+'\n[brine:done]\n');stream.finish();
        return { result:stream.status.result, retained:stream.status.invocation!==null, summary:stream.summary() };
      }
      results.push({operation,kind:'engine-observation',...parse({receipt,exitCode:0,status:'succeeded'})});
      if (operation === 'validate') {
        receipt.resources=[];receipt.outcome=receipt.effects='unknown';
        results.push({operation,kind:'empty-unknown',...parse({receipt,exitCode:1,status:'unknown'})});
      } else {
        receipt.resources=[{kind:'service',identityDigest:'sha256:'+'f'.repeat(64),action:'create',effect:'none'}];
        receipt.outcome='changes';receipt.changesDetected=true;
        results.push({operation,kind:'planned',...parse({receipt,exitCode:operation==='plan'?1:0,status:'changes'})});
        receipt.outcome=receipt.effects='unknown';receipt.errors=[{code:'ENGINE_CLOSE_FAILED',message:'Engine connection release failed. Inspect the target before retrying.'}];
        results.push({operation,kind:'close-failure',...parse({receipt,exitCode:1,status:'unknown'})});
      }
    }
    return results;
  });
  for (const row of results) {
    expect(row.result).toBe(row.kind==='planned'?'changes':'unknown');
    expect(row.retained).toBe(row.kind!=='engine-observation');
    if(row.kind==='close-failure' || row.kind==='empty-unknown') {
      expect(row.summary).toContain('Effects: unknown');
      expect(row.summary).toContain('Qualified identity digest (received): not received');
    }
  }
});
test.skip(!baseURL, 'Requires synthetic populated fixture, never a real engine');

for (const mode of ['succeeded', 'no-op', 'cleanup-failed', 'known-partial', 'conservative-unknown', 'truncated-complete', 'unknown-effects', 'missing-terminal', 'none-visible-complete', 'complete-visible-unknown', 'partial-visible-unknown']) {
  test(`qualified current-action receipt UI: ${mode}`, async ({ page }) => {
    // Synthetic producer-shaped stop/down observations, never native evidence.
    const operation = mode === 'known-partial' ? 'down' : 'stop';
    const receipt = {
      version: 'dill.operation/1', operationId: 'op_' + 'a'.repeat(32), operation, stack: 'observability',
      target: { engine: 'docker', endpointDigest: 'sha256:' + 'b'.repeat(64), qualifiedIdentityDigest: 'sha256:' + 'c'.repeat(64) },
      outcome: 'succeeded', effects: 'complete', changesDetected: false,
      resources: [{ kind: 'container', identityDigest: 'sha256:' + 'd'.repeat(64), action: operation === 'down' ? 'remove' : 'stop', effect: 'complete' }],
      resourcesTruncated: mode === 'truncated-complete', errors: [],
    };
    const invocation = { receipt, exitCode: 0, status: 'succeeded' };
    let expected = 'succeeded';
    if (mode === 'no-op') { receipt.effects = 'none'; receipt.resources = []; }
    if (mode === 'cleanup-failed') {
      receipt.errors = [{ code: 'COORDINATION_RELEASE_FAILED', message: 'Engine outcome was recorded; local coordination cleanup failed.' }];
      invocation.exitCode = 1; invocation.status = expected = 'failed';
    }
    if (mode === 'known-partial' || mode === 'partial-visible-unknown') {
      receipt.outcome = 'failed'; receipt.effects = 'partial';
      receipt.errors = [{ code: 'OPERATION_FAILED', message: 'Operation failed. Inspect the target before retrying if effects are uncertain.' }];
      invocation.exitCode = 1; invocation.status = expected = 'failed';
    }
    if (mode === 'conservative-unknown') {
      receipt.outcome = receipt.effects = 'unknown';
      receipt.errors = [{ code: 'ENGINE_CLOSE_FAILED', message: 'Engine connection release failed. Inspect the target before retrying.' }];
      invocation.exitCode = 1; invocation.status = expected = 'unknown';
    }
    if (mode === 'unknown-effects') receipt.effects = 'future';
    if (mode === 'none-visible-complete') receipt.effects = 'none';
    if (mode === 'complete-visible-unknown' || mode === 'partial-visible-unknown') receipt.resources[0].effect = 'unknown';
    if (['unknown-effects', 'missing-terminal', 'none-visible-complete', 'complete-visible-unknown', 'partial-visible-unknown'].includes(mode)) expected = 'unknown';
    const text = '[brine:operation] ' + JSON.stringify(invocation) + '\n' + (mode === 'missing-terminal' ? '' : invocation.status === 'succeeded' ? '[brine:done] complete\n' : '[brine:error] failed\n');
    let requests = 0;
    await page.route(`**/api/stacks/observability/${operation}`, async route => { requests++; await route.fulfill({ status: 200, body: text, contentType: 'text/plain' }); });
    await page.goto(new URL('/stacks/observability', baseURL).href);
    await page.locator('#action-output').evaluate(el => { el.dataset.qualifiedOperations = 'true'; });
    // Invoke the existing action directly to avoid the Down confirmation dialog;
    // this is synthetic stream rendering, not a destructive-route approval test.
    await page.evaluate(operation => runAction(operation, 'observability'), operation);
    await expect(page.locator('#action-status')).toHaveText(expected);
    const valid = ['succeeded', 'no-op', 'cleanup-failed', 'known-partial', 'conservative-unknown', 'truncated-complete'].includes(mode);
    if (valid) {
      await expect(page.locator('#action-log')).toContainText('Outcome: ' + receipt.outcome);
      await expect(page.locator('#action-log')).toContainText('Effects: ' + receipt.effects);
      await expect(page.locator('#action-log')).toContainText('Endpoint selector digest (not daemon identity)');
      await expect(page.locator('#action-log')).toContainText('Qualified identity digest (received)');
      if (mode !== 'no-op') await expect(page.locator('#action-log')).toContainText(receipt.resources[0].identityDigest);
    } else if (mode !== 'missing-terminal') {
      await expect(page.locator('#action-log')).toContainText('No validated terminal receipt');
      await expect(page.locator('#action-log')).not.toContainText('Operation ID:');
    }
    if (mode === 'cleanup-failed') await expect(page.locator('#action-log')).toContainText('Process exit: 1');
    if (mode === 'truncated-complete') await expect(page.locator('#action-log')).toContainText('Resource observations truncated');
    expect(requests).toBe(1);
  });
}
