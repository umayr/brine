const { test, expect } = require('@playwright/test');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

for (const mode of ['javascript-disabled', 'csp-blocked']) {
  test(`no native reveal request when ${mode}`, async ({browser}) => {
    const fixture=path.join(os.tmpdir(),`brine-nofallback-${process.pid}-${mode}.html`);
    const context=await browser.newContext({javaScriptEnabled:mode!=='javascript-disabled'});
    try {
      execFileSync('go',['test','./internal/remoteui','-run','^TestBrowserFixture$','-count=1'],{env:{...process.env,BRINE_REMOTE_FIXTURE:fixture}});
      const html=fs.readFileSync(fixture,'utf8');let reveals=0;
      await context.route('http://remote.test/**',async route=>{
        if(route.request().url().endsWith('/remote/reveal')) {reveals++;await route.fulfill({contentType:'application/json',body:'[{"key":"TOKEN","value":"secret-value"}]'});}
        else await route.fulfill({contentType:'text/html',headers:mode==='csp-blocked'?{'Content-Security-Policy':"script-src 'none'"}:{},body:html});
      });
      const page=await context.newPage();await page.goto('http://remote.test/remote/inspect');
      await expect(page.getByRole('button',{name:'Reveal values'})).toHaveCount(0);
      await expect(page.locator('#values')).toBeEmpty();expect(reveals).toBe(0);expect(page.url()).toContain('/remote/inspect');
    } finally {await context.close();if(fs.existsSync(fixture))fs.unlinkSync(fixture)}
  });
}
