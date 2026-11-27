// Synthetic authenticated fixture only. No provider/engine calls or deployments.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.BRINE_PLAYWRIGHT_MODULE || '@playwright/test');
const base = process.env.BRINE_BROKER_FIXTURE_URL || 'http://127.0.0.1:18089';
const output = path.resolve('.local/secrets-implementation/browser');
fs.mkdirSync(output, {recursive:true});
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
 for(const theme of ['dark','light']) for(const width of [1440,390]) {
  const context=await browser.newContext({viewport:{width,height:1000}});
  await context.addInitScript(theme=>localStorage.setItem('theme',theme),theme);
  const page=await context.newPage();
  await page.goto(base+'/settings/secrets');
  await page.locator('input[name=user]').fill('admin');
  await page.locator('input[name=password]').fill('fixture-only-not-a-secret');
  await Promise.all([page.waitForURL('**/settings/secrets'),page.getByRole('button',{name:'Sign in',exact:true}).click()]);
  assert.equal(await page.getByText('Configured · not tested',{exact:true}).count(),3);
  for(const provider of ['Vault KV v2','Infisical','Local files']) assert.ok(await page.getByText(provider,{exact:true}).isVisible());
  assert.ok(!(await page.content()).includes('/operator/private'));
  await page.screenshot({path:path.join(output,`providers-${theme}-${width}.png`),fullPage:true});
  await page.goto(base+'/stacks/homepage#variables');
  const panel=page.locator('section[aria-labelledby=stack-secrets-title]');
  assert.ok(await panel.isVisible());
  assert.equal(await page.getByRole('tab',{name:'Secrets',exact:true}).count(),0);
  for(const provider of ['files','vault','infisical']) {
   await panel.locator('summary').click();
   const form=panel.locator('form').filter({has:page.getByRole('button',{name:'Save reference',exact:true})});
   const old=await form.locator('input[name=revision]').inputValue();
   await form.locator('input[name=name]').fill('ui_password');
   await form.locator('select[name=connection]').selectOption(provider);
   if(provider==='files') await form.locator('input[name=alias]').fill('database-password');
   if(provider==='vault') {await form.locator('input[name=vaultPath]').fill('app/database');await form.locator('input[name=property]').fill('password');await form.locator('input[name=version]').fill('3');}
   if(provider==='infisical') {await form.locator('input[name=folder]').fill('/apps');await form.locator('input[name=key]').fill('DATABASE_PASSWORD');await form.locator('input[name=version]').fill('2');}
   await Promise.all([page.waitForNavigation(),form.getByRole('button',{name:'Save reference',exact:true}).click()]);
   assert.ok((await panel.textContent()).includes('ui_password'));
   const status=await page.evaluate(async(old)=>{
    const f=document.querySelector('section[aria-labelledby=stack-secrets-title] form');
    const body=new URLSearchParams(new FormData(f));body.set('action','delete');body.set('revision',old);
    return (await fetch(f.getAttribute('action'),{method:'POST',body})).status;
   },old);
   assert.equal(status,409,'stale binding edit rejected');
  }
  const forbidden=await page.evaluate(async()=>{
   const f=document.querySelector('section[aria-labelledby=stack-secrets-title] form');const body=new URLSearchParams(new FormData(f));body.set('action','delete');body.set('value','do-not-store');return (await fetch(f.getAttribute('action'),{method:'POST',body})).status;
  });assert.equal(forbidden,400,'value field rejected');
  const csrf=await page.evaluate(async()=>{const f=document.querySelector('section[aria-labelledby=stack-secrets-title] form');const body=new URLSearchParams(new FormData(f));body.set('action','delete');body.delete('_csrf');return (await fetch(f.getAttribute('action'),{method:'POST',body})).status;});assert.equal(csrf,403,'CSRF required');
  await page.screenshot({path:path.join(output,`variables-${theme}-${width}.png`),fullPage:true});
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'no horizontal overflow');
  await Promise.all([page.waitForNavigation(),panel.getByRole('button',{name:'Remove reference',exact:true}).click()]);
  assert.ok((await panel.textContent()).includes('No references saved'));
  await page.getByRole('tab',{name:'History',exact:true}).click();
  await page.getByRole('heading',{name:'Secrets and bindings',exact:true}).waitFor();
  assert.ok((await page.locator('#stack-source-history').textContent()).includes('Binding revision'));
  assert.ok(!(await page.locator('#stack-source-history').textContent()).includes('/operator/private'));
  await page.screenshot({path:path.join(output,`history-${theme}-${width}.png`),fullPage:true});
  await page.locator('#plan-button').click();
  await page.locator('#panel-Plan:not([hidden])').waitFor();
  await page.getByRole('heading',{name:'Plan · source and secret preparation',exact:true}).waitFor();
  assert.ok((await page.locator('#plan-result').textContent()).includes('.brine-broker-inputs.json'));
  assert.ok(!(await page.locator('#plan-result').textContent()).includes('fixture-only-not-a-secret'));
  assert.equal(await page.getByRole('tab',{name:'Plan',exact:true}).getAttribute('aria-selected'),'true');
  assert.ok(!(await page.locator('#plan-result').textContent()).includes('deployed'));
  await page.screenshot({path:path.join(output,`plan-inputs-${theme}-${width}.png`),fullPage:true});
  await context.close();
 }
 console.log('PASS: 4 authenticated desktop/mobile theme flows, three providers, CAS, forbidden values, CSRF, remove, shared binding History, no extra tab/overflow; Plan opens/focuses with captured non-secret input consent; 16 screenshots');
 } finally {await browser.close();}
})().catch(err=>{console.error(err);process.exitCode=1});
