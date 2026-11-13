const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL, 'Requires synthetic populated fixture, never a real engine');

for (const mode of ['succeeded', 'no-op', 'cleanup-failed', 'unknown-effects', 'missing-terminal']) {
  test(`qualified current-action receipt UI: ${mode}`, async ({ page }) => {
    // Positive shape emulates the real producer contract; not native acceptance.
    const receipt = {version:'dill.operation/1',operationId:'op_'+'a'.repeat(32),operation:'up',stack:'observability',
      target:{engine:'docker',endpointDigest:'sha256:'+'b'.repeat(64),qualifiedIdentityDigest:'sha256:'+'c'.repeat(64)},
      outcome:'succeeded',effects:mode==='unknown-effects'?'future':mode==='no-op'?'none':'complete',changesDetected:false,resources:[],resourcesTruncated:false,
      errors:mode==='cleanup-failed'?[{code:'COORDINATION_RELEASE_FAILED',message:'Engine outcome was recorded; local coordination cleanup failed.'}]:[]};
    const invocation={receipt,exitCode:mode==='cleanup-failed'?1:0,status:mode==='cleanup-failed'?'failed':'succeeded'};
    const text='[brine:operation] '+JSON.stringify(invocation)+'\n'+(mode==='missing-terminal'?'':mode==='cleanup-failed'?'[brine:error] failed\n':'[brine:done] complete\n');
    let requests=0;
    await page.route('**/api/stacks/observability/up',async route=>{requests++;await route.fulfill({status:200,body:text,contentType:'text/plain'});});
    await page.goto(new URL('/stacks/observability',baseURL).href);
    await page.locator('#action-output').evaluate(el=>{el.dataset.qualifiedOperations='true';});
    await page.getByRole('button',{name:'Deploy',exact:true}).click();
    await expect(page.locator('#action-status')).toHaveText(['succeeded','no-op'].includes(mode)?'succeeded':mode==='cleanup-failed'?'failed':'unknown');
    if(['succeeded','no-op','cleanup-failed'].includes(mode)){
      await expect(page.locator('#action-log')).toContainText('Outcome: succeeded');
      await expect(page.locator('#action-log')).toContainText(mode==='no-op'?'Effects: none':'Effects: complete');
      await expect(page.locator('#action-log')).toContainText('Endpoint selector digest (not daemon identity)');
      await expect(page.locator('#action-log')).toContainText('Qualified identity digest (received)');
    }
    if(mode==='cleanup-failed')await expect(page.locator('#action-log')).toContainText('Process exit: 1');
    expect(requests).toBe(1);
  });
}
