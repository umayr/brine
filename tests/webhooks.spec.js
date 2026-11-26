const { test, expect } = require('@playwright/test');
const root = process.env.BRINE_WEBHOOK_FIXTURE_BASE_URL;
test.skip(!root, 'Requires the disposable authenticated webhook fixture');

test('Webhooks manages real persisted configuration without executing or fabricating deliveries', async ({ browser }) => {
 const context = await browser.newContext({viewport:{width:1440,height:1000}}), page = await context.newPage(), errors=[];
 page.on('pageerror',e=>errors.push(e.message));
 await page.goto(root+'/login?next=%2Fsettings%2Fwebhooks');
 await page.getByLabel('Username',{exact:true}).fill('admin');await page.getByLabel('Password',{exact:true}).fill('fixture-only-not-a-secret');await page.getByRole('button',{name:'Sign in',exact:true}).click();
 await page.waitForURL('**/settings/webhooks');await expect(page.getByRole('heading',{name:'Your next connection starts here'})).toBeVisible();
 await page.getByRole('button',{name:'+ Add endpoint',exact:true}).click();
 await page.getByLabel('Name',{exact:true}).fill('Browser receiver');await page.getByLabel('Destination URL',{exact:true}).fill('https://hooks.example.test/brine');
 await page.getByRole('button',{name:'Save endpoint',exact:true}).click();await expect(page.getByRole('alert')).toContainText('Select at least one event');
 await page.getByRole('checkbox',{name:'operation.completed',exact:true}).check();await page.getByRole('button',{name:'Save endpoint',exact:true}).click();
 const dialog=page.getByRole('dialog');await expect(dialog.getByRole('heading',{name:'Save this signing secret'})).toBeVisible();const secret=await dialog.getByLabel('Signing secret',{exact:true}).inputValue();expect(secret).toMatch(/^[a-f0-9]{64}$/);await dialog.getByRole('button',{name:'Reveal',exact:true}).click();await expect(dialog.getByLabel('Signing secret',{exact:true})).toHaveAttribute('type','text');await dialog.getByRole('button',{name:'Done',exact:true}).click();expect((await page.content()).includes(secret)).toBe(false);
 await expect(page.getByRole('heading',{name:'Browser receiver',exact:true})).toBeVisible();
 const csrf=await page.locator('meta[name=csrf-token]').getAttribute('content');
 expect((await context.request.post(root+'/api/webhooks/endpoints',{data:{name:'No CSRF'}})).status()).toBe(403);
 const inventory=await context.request.get(root+'/api/webhooks/');expect(inventory.status()).toBe(200);expect((await inventory.text()).includes(secret)).toBe(false);
 await page.getByRole('button',{name:'Send test',exact:true}).click();await dialog.getByRole('button',{name:'Confirm',exact:true}).click();
 await expect(page.getByRole('heading',{name:'Delivery history',exact:true})).toBeVisible();await page.getByRole('button',{name:'webhook.test',exact:true}).click();await expect(dialog).toContainText('No attempts yet');await expect(dialog).toContainText('pending');await page.getByRole('button',{name:'Close dialog',exact:true}).click();
 await page.locator('#hooks-tabs').getByRole('link',{name:'Incoming triggers',exact:true}).click();await page.getByRole('button',{name:'+ Create trigger',exact:true}).click();await page.getByLabel('Name',{exact:true}).fill('Browser pipeline');await page.getByLabel('Allowed action',{exact:true}).selectOption('plan');await page.getByRole('button',{name:'Review trigger',exact:true}).click();await expect(dialog).toContainText('homepage');await expect(dialog).toContainText('Local (synthetic)');await dialog.getByRole('button',{name:'Confirm',exact:true}).click();await expect(dialog.getByRole('heading',{name:'Save this signing secret'})).toBeVisible();await dialog.getByRole('button',{name:'Done',exact:true}).click();await expect(page.getByRole('heading',{name:'Browser pipeline',exact:true})).toBeVisible();
 for(const theme of ['light','dark'])for(const width of [1440,390]){await page.evaluate(t=>window.setBrineTheme(t),theme);await page.setViewportSize({width,height:1000});for(const section of ['outgoing','incoming','deliveries','catalog']){await page.goto(root+'/settings/webhooks#'+section);await expect(page.locator('#hooks-view')).toHaveAttribute('aria-busy','false');await page.evaluate(t=>window.setBrineTheme(t),theme);await expect(page.locator('html')).toHaveAttribute('data-theme',theme);expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);await page.screenshot({path:test.info().outputPath(`webhooks-${section}-${theme}-${width}.png`),fullPage:true});}}
 await page.goto(root+'/settings/webhooks#outgoing');await expect(page.getByRole('heading',{name:'Browser receiver',exact:true})).toBeVisible();await page.getByRole('button',{name:'Pause',exact:true}).click();await expect(page.getByRole('button',{name:'Enable',exact:true})).toBeVisible();
 // Verify unavailable is not empty through a failing read, not fake success.
 await page.route('**/api/webhooks/',r=>r.fulfill({status:503,contentType:'application/json',body:'{"error":"Fixture read unavailable"}'}));await page.getByRole('button',{name:'Refresh',exact:true}).click();await expect(page.getByRole('heading',{name:'Webhook configuration unavailable'})).toBeVisible();
 expect(errors).toEqual([]);expect(csrf).toBeTruthy();await context.close();
});
