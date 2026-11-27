const {test,expect}=require('@playwright/test');
const root=process.env.BRINE_REMOTE_WEBHOOK_BASE_URL;
test.skip(!root,'Requires explicit disposable Linux/Dill incoming fixture');
test('Settings reviews a real capability-scoped remote lifecycle binding',async({browser})=>{
 test.setTimeout(120000);
 const access=process.env.BRINE_QUAL_ACCESS;expect(access).toMatch(/^[a-f0-9]{64}$/);
 const context=await browser.newContext({viewport:{width:1440,height:1000},extraHTTPHeaders:{'X-Brine-Qualification':access}}),page=await context.newPage(),errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(root+'/login?next=%2Fsettings%2Fwebhooks');await page.getByLabel('Username',{exact:true}).fill('admin');await page.getByLabel('Password',{exact:true}).fill('fixture-only-password');await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.waitForURL('**/settings/webhooks');
 const sourceResponse=await context.request.get(root+'/__qualification/git-source'),sourceInfo=await sourceResponse.json();const setupStack=sourceInfo.stack;expect(setupStack).toMatch(/^brine-it-lifecycle-/);
 const beforeApproval=await context.request.get(root+'/api/webhooks/');expect(beforeApproval.status()).toBe(200);const unapproved=await beforeApproval.json();expect(unapproved.options).toEqual([]);
 await page.goto(root+'/remote/deployment?host=synthetic');await page.getByLabel('Git-backed Brine project',{exact:true}).fill(setupStack);await page.getByRole('button',{name:'Remote auto-deploy settings',exact:true}).click();await expect(page.locator('body')).toContainText('Branch: main');await expect(page.locator('body')).toContainText('Selected target: synthetic');await page.getByRole('checkbox',{name:/^Authorize this Git branch to send exact commits/}).check();await page.getByRole('button',{name:'Enable remote auto-deploy',exact:true}).click();
 await expect.poll(async()=>{const r=await context.request.get(root+'/api/webhooks/');const data=await r.json();return data.options.some(row=>row.stack_name===setupStack);},{timeout:30000}).toBe(true);
 await page.goto(root+'/settings/webhooks');
 await page.locator('#hooks-tabs').getByRole('link',{name:'Incoming triggers',exact:true}).click();await page.getByRole('button',{name:'+ Create trigger',exact:true}).click();
 await page.getByLabel('Name',{exact:true}).fill('Browser remote pipeline');
 const binding=page.getByLabel('Stack / execution target',{exact:true});await expect(binding.locator('option')).toHaveCount(1);await expect(binding).toContainText('brine-it-lifecycle-');await expect(binding).toContainText('synthetic');const actualStack=(await binding.locator('option').first().textContent()).split(' → ')[0];
 const actions=page.getByLabel('Allowed action',{exact:true});expect(await actions.locator('option').allTextContents()).toEqual(['plan','git-sync','deploy','start','stop','restart']);await actions.selectOption('stop');
 await page.getByRole('button',{name:'Review trigger',exact:true}).click();const dialog=page.getByRole('dialog');await expect(dialog).toContainText('synthetic');await expect(dialog).toContainText('stop');await dialog.getByRole('button',{name:'Confirm',exact:true}).click();await expect(dialog.getByRole('heading',{name:'Save this signing secret'})).toBeVisible();
 const secret=await dialog.getByLabel('Signing secret',{exact:true}).inputValue();expect(secret).toMatch(/^[a-f0-9]{64}$/);await dialog.getByRole('button',{name:'Done',exact:true}).click();expect((await page.content()).includes(secret)).toBe(false);
 const card=page.locator('article').filter({has:page.getByRole('heading',{name:'Browser remote pipeline',exact:true})});await expect(card).toContainText('synthetic');await expect(card).toContainText('stop');
 const csrf=await page.locator('meta[name=csrf-token]').getAttribute('content');expect(csrf).toBeTruthy();expect((await context.request.post(root+'/api/webhooks/triggers',{data:{name:'Denied'}})).status()).toBe(403);
 for(const theme of ['light','dark'])for(const width of [1440,390]){await page.evaluate(t=>window.setBrineTheme(t),theme);await page.setViewportSize({width,height:1000});await expect(card).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);await page.screenshot({path:test.info().outputPath(`remote-binding-${theme}-${width}.png`),fullPage:true});}
 expect(errors).toEqual([]);await card.getByRole('button',{name:'Disable',exact:true}).click();await dialog.getByRole('button',{name:'Confirm',exact:true}).click();await expect(card.getByRole('button',{name:'Enable',exact:true})).toBeVisible();
 await expect.poll(async()=>{const r=await context.request.get(root+'/api/webhooks/requests');const data=await r.json();return data.requests.some(row=>row.idempotency_key==='upstream-unavailable'&&row.status==='unknown');},{timeout:30000}).toBe(true);
 // Use the real production form navigation, not injected HTML or fabricated auth.
 await page.goto(root+'/remote/deployment?host=synthetic');await page.getByLabel('Git-backed Brine project',{exact:true}).fill(actualStack);await page.getByRole('button',{name:'Remote auto-deploy settings',exact:true}).click();
 await page.getByLabel('I inspected this source sync; disable automation without retrying it.',{exact:true}).check();await page.getByRole('button',{name:'Confirm source inspection',exact:true}).click();await expect(page.getByRole('button',{name:'Enable remote auto-deploy',exact:true})).toBeVisible();await expect(page.locator('body')).toContainText('State: disabled');expect(errors).toEqual([]);
 // Separate workspace: both attended confirmations are explicit production UI
 // actions. The Go harness changes synthetic source only after proven creation.
 const manualStack=actualStack+'-manual',captured=[];
 for(const mode of ['create','update']){
  await page.goto(root+'/remote/deployment?host=synthetic');await page.getByLabel('Brine project name',{exact:true}).fill(manualStack);await page.getByRole('button',{name:'Review project files',exact:true}).click();await expect(page.getByRole('heading',{name:'Review captured files',exact:true})).toBeVisible();await expect(page.locator('body')).toContainText('No files have been sent yet.');const entry=page.getByRole('listitem').filter({hasText:/^compose\.pkl ·/});await expect(entry).toHaveCount(1);const digest=await entry.locator('code').textContent();expect(digest).toMatch(/^[a-f0-9]{64}$/);captured.push(digest);
  await page.getByLabel('Send these exact files and request a preview',{exact:true}).check();await page.getByRole('button',{name:'Stage and preview',exact:true}).click();await expect(page.getByRole('heading',{name:'Deployment preview for '+manualStack,exact:true})).toBeVisible();await expect(page.locator('body')).toContainText('This preview did not permit writes.');
  await page.getByLabel(`Confirm ${mode} for this stack on this target`,{exact:true}).check();await page.getByRole('button',{name:mode==='create'?'Create stack':'Update stack',exact:true}).click();await expect(page.locator('body')).toContainText('State succeeded · Effects complete');const request=await page.getByLabel('Request ID',{exact:true}).inputValue();expect(request).toMatch(/^[a-f0-9]{32}$/);
  await page.getByRole('link',{name:'Refresh status',exact:true}).click();await expect(page.getByLabel('Request ID',{exact:true})).toHaveValue(request);await expect(page.locator('body')).toContainText('State succeeded · Effects complete');
  // Existing explicit recovery inspection releases the local intent, even for
  // an already known successful receipt. It fences/checks, never reapplies.
  await page.getByRole('checkbox',{name:/^Check the original journal and prevent/}).check();await page.getByRole('button',{name:'Check and fence request',exact:true}).click();await expect(page.locator('body')).toContainText('State succeeded · Effects complete');
  await expect.poll(async()=>{const r=await context.request.get(root+'/__qualification/manual-phase');return (await r.json()).phase;},{timeout:45000}).toBe(mode==='create'?1:2);
 }
 expect(captured[0]).not.toEqual(captured[1]);expect(errors).toEqual([]);await context.close();
 await require('./helpers/source-restore.cjs')(browser,root,access,actualStack+'-restore',test.info());
});
