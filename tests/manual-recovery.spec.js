const { test, expect } = require('@playwright/test');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

test('manual recovery requires confirmation and posts only the saved request to fencing', async ({page}) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'brine-manual-recovery-'));
  try {
    execFileSync('go', ['test','./internal/remoteui','-run','^TestDeploymentStatusBrowserFixture$','-count=1'], {env:{...process.env,BRINE_DEPLOYMENT_STATUS_FIXTURE:dir}});
    const id='1a23456789abcdef0123456789abcdef';
    const requests=[];
    await page.route('http://remote.test/**', route => {
      const req=route.request();
      requests.push({method:req.method(),url:req.url(),body:req.postData()});
      return route.fulfill({contentType:'text/html',path:path.join(dir,req.method()==='POST'?'partial.html':'recover.html')});
    });
    await page.setViewportSize({width:320,height:640});
    await page.goto(`http://remote.test/remote/deployment?host=demo&id=${id}`);
    const button=page.getByRole('button',{name:'Check and fence request'});
    await button.click();
    expect(requests).toHaveLength(1);
    await page.getByRole('checkbox').check();
    await button.click();
    await expect(page.getByRole('alert')).toContainText('partially changing the target');
    expect(requests).toHaveLength(2);
    expect(requests[1].method).toBe('POST');
    expect(requests[1].url).toBe('http://remote.test/remote/deployment/recover');
    expect(Object.fromEntries(new URLSearchParams(requests[1].body))).toEqual({_csrf:'synthetic-csrf',host:'demo',id,confirm:'check-and-fence'});
    await expect(page.locator('form[method="post"]')).toHaveCount(0);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
  } finally { fs.rmSync(dir,{recursive:true,force:true}); }
});
