const {test,expect}=require('@playwright/test');
const root=process.env.BRINE_REMOTE_WEBHOOK_BASE_URL;
test.skip(!root,'Requires explicit disposable Linux/Dill incoming fixture');
test('Settings reviews a real capability-scoped remote lifecycle binding',async({browser})=>{
 const context=await browser.newContext({viewport:{width:1440,height:1000}}),page=await context.newPage(),errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(root+'/login?next=%2Fsettings%2Fwebhooks');await page.getByLabel('Username',{exact:true}).fill('admin');await page.getByLabel('Password',{exact:true}).fill('fixture-only-password');await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.waitForURL('**/settings/webhooks');
 await page.locator('#hooks-tabs').getByRole('link',{name:'Incoming triggers',exact:true}).click();await page.getByRole('button',{name:'+ Create trigger',exact:true}).click();
 await page.getByLabel('Name',{exact:true}).fill('Browser remote pipeline');
 const binding=page.getByLabel('Stack / execution target',{exact:true});await expect(binding.locator('option')).toHaveCount(1);await expect(binding).toContainText('brine-it-lifecycle-');await expect(binding).toContainText('synthetic');
 const actions=page.getByLabel('Allowed action',{exact:true});expect(await actions.locator('option').allTextContents()).toEqual(['start','stop','restart']);await actions.selectOption('stop');
 await page.getByRole('button',{name:'Review trigger',exact:true}).click();const dialog=page.getByRole('dialog');await expect(dialog).toContainText('synthetic');await expect(dialog).toContainText('stop');await dialog.getByRole('button',{name:'Confirm',exact:true}).click();await expect(dialog.getByRole('heading',{name:'Save this signing secret'})).toBeVisible();
 const secret=await dialog.getByLabel('Signing secret',{exact:true}).inputValue();expect(secret).toMatch(/^[a-f0-9]{64}$/);await dialog.getByRole('button',{name:'Done',exact:true}).click();expect((await page.content()).includes(secret)).toBe(false);
 const card=page.locator('article').filter({has:page.getByRole('heading',{name:'Browser remote pipeline',exact:true})});await expect(card).toContainText('synthetic');await expect(card).toContainText('stop');
 const csrf=await page.locator('meta[name=csrf-token]').getAttribute('content');expect(csrf).toBeTruthy();expect((await context.request.post(root+'/api/webhooks/triggers',{data:{name:'Denied'}})).status()).toBe(403);
 for(const theme of ['light','dark'])for(const width of [1440,390]){await page.evaluate(t=>window.setBrineTheme(t),theme);await page.setViewportSize({width,height:1000});await expect(card).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);await page.screenshot({path:test.info().outputPath(`remote-binding-${theme}-${width}.png`),fullPage:true});}
 expect(errors).toEqual([]);await card.getByRole('button',{name:'Disable',exact:true}).click();await dialog.getByRole('button',{name:'Confirm',exact:true}).click();await expect(card.getByRole('button',{name:'Enable',exact:true})).toBeVisible();await context.close();
});
