const {test,expect}=require('@playwright/test');
const base=process.env.BRINE_FIXTURE_BASE_URL;
test('Live Updates is a sibling of Appearance; settings persist and reach open stack tabs',async({page,context})=>{
 await page.goto(base+'/settings');
 await page.getByRole('link',{name:'Live Updates',exact:true}).click();
 await expect(page.getByRole('link',{name:'Live Updates',exact:true})).toHaveAttribute('aria-current','page');
 const enabled=page.getByLabel('Auto-refresh'), interval=page.getByLabel('Refresh interval');
 await expect(enabled).toBeChecked();await expect(interval).toHaveValue('5');
 await interval.selectOption('30');await enabled.uncheck();await expect(interval).toBeHidden();
 await page.reload();await expect(enabled).not.toBeChecked();await expect(interval).toBeHidden();
 const stack=await context.newPage();
 let reads=0;await stack.route('**/web/stacks/homepage/containers',route=>{reads++;return route.fulfill({contentType:'text/html',body:'<p>Inventory sample '+reads+'</p>'})});
 await stack.goto(base+'/stacks/homepage'); await stack.clock.install();
 await stack.clock.fastForward(60000); expect(reads).toBe(0);
 await stack.getByRole('tab',{name:'Containers'}).click();
 await expect(stack.locator('#stack-containers')).toHaveText('Inventory sample 1');
 await stack.clock.fastForward(60000);expect(reads).toBe(1);
 await stack.getByRole('button',{name:'Refresh',exact:true}).click();await expect(stack.locator('#stack-containers')).toHaveText('Inventory sample 2');
 await page.getByRole('button',{name:'Reset live updates'}).click();await expect(interval).toHaveValue('5');
 await expect.poll(()=>stack.evaluate(()=>localStorage.getItem('brine-live-updates'))).toBe(null);
 await stack.clock.runFor(5200);await expect(stack.locator('#stack-containers')).toHaveText('Inventory sample 3');
 await stack.getByRole('tab',{name:'Compose'}).click();await stack.clock.fastForward(10000);expect(reads).toBe(3);
 await stack.close();
});
test('a conflicted first plan blocks deployment until a fresh tagged response',async({page})=>{
 let conflict=true;
 await page.route('**/web/stacks/media/plan',r=>r.fulfill(conflict?
  {status:409,headers:{'X-Brine-Plan-Stale':'true'},body:'Replan'}:
  {headers:{'Content-Type':'text/html','X-Brine-Plan-Inputs':'f'.repeat(64)},body:'<p>Fresh snapshot</p>'}));
 await page.goto(base+'/stacks/media');await page.locator('#plan-button').click();
 await expect(page.locator('#deploy-button')).toBeDisabled();await expect(page.locator('#plan-stale')).toBeVisible();
 conflict=false;await page.locator('#plan-button').click();await expect(page.locator('#plan-result')).toHaveText('Fresh snapshot');
 await expect(page.locator('#deploy-button')).toBeEnabled();await expect(page.locator('#plan-stale')).toBeHidden();
 await page.evaluate(()=>window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true})));
 await expect(page.locator('#deploy-button')).toBeDisabled();await page.locator('#plan-button').click();await expect(page.locator('#deploy-button')).toBeEnabled();
});
test('an observed source change keeps the old diff marked stale and blocks Deploy until a fresh plan',async({page})=>{
 await page.route('**/web/stacks/media/plan',r=>r.fulfill({headers:{'Content-Type':'text/html','X-Brine-Plan-Inputs':'a'.repeat(64)},body:'<p>Prior snapshot</p>'}));
 await page.route('**/web/stacks/media/git/pull',r=>r.fulfill({contentType:'text/html',body:'Updated'}));
 await page.goto(base+'/stacks/media');await page.locator('#plan-button').click();await expect(page.locator('#plan-result')).toHaveText('Prior snapshot');
 await page.getByRole('tab',{name:'Compose'}).click();await page.getByRole('button',{name:'Pull latest'}).click();
 await page.getByRole('tab',{name:'Plan',exact:true}).click();await expect(page.locator('#plan-stale')).toBeVisible();
 await expect(page.locator('#deploy-button')).toBeDisabled();await expect(page.locator('#plan-result')).toContainText('Prior snapshot');
 await page.locator('#plan-button').click();await expect(page.locator('#plan-stale')).toBeHidden();await expect(page.locator('#deploy-button')).toBeEnabled();
});
