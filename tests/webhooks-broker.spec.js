const {test,expect}=require('@playwright/test');
const root=process.env.BRINE_BROKER_WEBHOOK_BASE_URL;
test.skip(!root,'Requires explicit disposable production broker fixture');
test('Production broker review produces redacted notifications without unattended authority',async({browser})=>{
 test.setTimeout(150000);
 const access=process.env.BRINE_QUAL_ACCESS;expect(access).toMatch(/^[a-f0-9]{64}$/);
 const context=await browser.newContext({viewport:{width:390,height:1100},extraHTTPHeaders:{'X-Brine-Qualification':access}});
 try {
 const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(root+'/login?next=%2Fsettings%2Fsecrets');
 await page.getByLabel('Username',{exact:true}).fill('broker-operator');await page.getByLabel('Password',{exact:true}).fill('fixture-only-password');await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.waitForURL('**/settings/secrets');await page.evaluate(()=>window.setBrineTheme('dark'));
 const info=await(await context.request.get(root+'/fixture/info')).json();expect(info.stack).toMatch(/^brine-it-broker-[a-f0-9]{16}$/);
 await expect(page.getByRole('heading',{name:'runtime-files',exact:true})).toBeVisible();
 const setupCSRF=await page.locator('meta[name=csrf-token]').getAttribute('content');
 const destination=await context.request.post(root+'/api/webhooks/endpoints',{headers:{'X-CSRF-Token':setupCSRF},data:{name:'Disposable broker receiver',url:info.receiver_url,events:['operation.completed'],enabled:true}});expect(destination.status()).toBe(201);
 const destinationGrant=await destination.json();expect(destinationGrant.secret).toMatch(/^[a-f0-9]{64}$/);expect((await context.request.post(root+'/fixture/receiver-key',{data:{secret:destinationGrant.secret}})).status()).toBe(204);
 await page.goto(root+'/stacks/'+info.stack);await page.getByRole('tab',{name:'Variables',exact:true}).click();await page.getByText('Add or update a reference',{exact:true}).click();
 await page.getByLabel('Logical secret name',{exact:true}).fill('password');await page.locator('form select[name=connection]').selectOption('runtime-files');await page.getByLabel('Allowlisted local file alias',{exact:true}).fill('password');await page.getByRole('button',{name:'Save reference',exact:true}).click();await page.getByRole('tab',{name:'Variables',exact:true}).click();await expect(page.getByText('Local alias · password',{exact:true})).toBeVisible();
 await page.locator('#plan-button').click();await expect(page.getByRole('heading',{name:'Plan · source and secret preparation',exact:true})).toBeVisible();expect(await page.getByRole('tab',{name:'Plan',exact:true}).getAttribute('aria-selected')).toBe('true');
 const preparation=page.waitForResponse(r=>r.url().endsWith('/broker/prepare'));await page.getByRole('button',{name:'Prepare references and Plan',exact:true}).click();expect((await preparation).status()).toBe(200);
 await expect(page.getByRole('button',{name:'Deploy reviewed Plan',exact:true})).toBeVisible();await expect(page.getByText('/run/secrets/password',{exact:true}).first()).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
 await page.screenshot({path:test.info().outputPath('broker-reviewed-dark-390.png'),fullPage:true});
 expect((await context.request.get(root+'/fixture/before-confirm')).status()).toBe(200);
 const confirmed=page.waitForResponse(r=>r.url().endsWith('/broker/confirm'));await page.getByRole('button',{name:'Deploy reviewed Plan',exact:true}).click();expect((await confirmed).status()).toBe(200);await expect(page.getByText('Target reported succeeded;',{exact:false})).toBeVisible();
 expect((await context.request.get(root+'/fixture/after-apply')).status()).toBe(200);
 await page.getByRole('tab',{name:'History',exact:true}).click();await expect(page.locator('#stack-history-content').getByText('succeeded',{exact:true}).first()).toBeVisible();
 const history=page.locator('#stack-history-content');
 // Read the exact request via the production history form's immutable record.
 const text=await history.innerText();const matched=text.match(/Request\s+([a-f0-9]{32})/);expect(matched).toBeTruthy();
 const csrf=await page.locator('meta[name=csrf-token]').getAttribute('content');expect(csrf).toBeTruthy();
 for(let n=0;n<2;n++){const status=await context.request.post(root+'/web/stacks/'+info.stack+'/broker/status',{headers:{'X-CSRF-Token':csrf},form:{request:matched[1],registration:info.registration}});expect(status.status()).toBe(200);expect(await status.text()).toContain('succeeded');}
 const denied=await context.request.post(root+'/web/stacks/'+info.stack+'/history/review',{headers:{'X-CSRF-Token':csrf},form:{kind:'retain'}});expect(denied.status()).toBe(409);expect(await denied.text()).toContain('Source change blocked.');
 const options=await context.request.get(root+'/api/webhooks/');expect(options.status()).toBe(200);const body=await options.json();expect(body.options_available).toBe(true);expect(body.options.some(o=>o.stack_name===info.stack)).toBe(false);expect(body.options.some(o=>o.stack_name===info.plain)).toBe(true);
 const target=body.options.find(o=>o.stack_name===info.plain).target_id;
 for(const action of ['plan','git-sync','deploy','start','stop','restart']){const deniedTrigger=await context.request.post(root+'/api/webhooks/triggers',{headers:{'X-CSRF-Token':csrf},data:{name:'Denied protected '+action,stack_id:info.stack_id,target_id:target,action,enabled:true}});expect(deniedTrigger.status()).toBe(403);}
 const noGrants=await(await context.request.get(root+'/api/webhooks/')).json();expect(noGrants.triggers).toEqual([]);
 const allowed=body.options.find(o=>o.stack_name===info.plain);const positive=await context.request.post(root+'/api/webhooks/triggers',{headers:{'X-CSRF-Token':csrf},data:{name:'Eligible plain control',stack_id:allowed.stack_id,target_id:allowed.target_id,action:'plan',enabled:true,requests_per_minute:10}});expect(positive.status()).toBe(201);const grant=(await positive.json()).trigger;expect((await context.request.delete(root+'/api/webhooks/triggers/'+grant.id,{headers:{'X-CSRF-Token':csrf},data:{version:grant.version}})).status()).toBe(204);
 await page.setViewportSize({width:1440,height:1000});await page.evaluate(()=>window.setBrineTheme('light'));await page.screenshot({path:test.info().outputPath('broker-history-light-1440.png'),fullPage:true});expect(errors).toEqual([]);
 await expect.poll(async()=>{const data=await(await context.request.get(root+'/api/webhooks/')).json();return data.deliveries.filter(d=>d.endpoint_id===destinationGrant.endpoint.id&&d.status==='delivered').length;},{timeout:15000}).toBe(2);
 expect((await context.request.post(root+'/fixture/finish')).status()).toBe(200);
 } finally {await context.request.post(root+'/fixture/finish').catch(()=>{});await context.close();}
});
