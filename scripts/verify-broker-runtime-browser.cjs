// Explicit disposable Linux fixture only: mutual TLS, production auth/router,
// real file provider and actual reviewed local Docker target. No secret values
// or session credentials are exported to this process or diagnostic output.
const assert=require('node:assert/strict');
const fs=require('node:fs');const crypto=require('node:crypto');
const {chromium}=require(process.env.BRINE_BROKER_BROWSER_MODULE);
(async()=>{
 const [base,clientCert,clientKey,serverCert,out]=process.argv.slice(2);
 const leaf=new crypto.X509Certificate(fs.readFileSync(serverCert));
 const pin=crypto.createHash('sha256').update(leaf.publicKey.export({format:'der',type:'spki'})).digest('base64');
 const browser=await chromium.launch({headless:true,args:['--ignore-certificate-errors-spki-list='+pin]});
 try {
  const context=await browser.newContext({viewport:{width:390,height:1100},clientCertificates:[{origin:base,certPath:clientCert,keyPath:clientKey}]});
  await context.addInitScript(()=>localStorage.setItem('theme','dark'));
  const login=await context.request.get(base+'/fixture/login',{maxRedirects:0});
  assert.equal(login.status(),303,'mutually authenticated fixture login failed');
  const info=await (await context.request.get(base+'/fixture/info')).json();
  const page=await context.newPage();page.setDefaultTimeout(35000);
  assert.match(info.stack,/^brokerui[a-f0-9]{16}$/);
  await page.goto(base+'/settings/secrets');await page.getByRole('heading',{name:'Provider connections',exact:true}).waitFor();
  assert.ok(await page.getByRole('heading',{name:'runtime-files',exact:true}).isVisible());
  await page.goto(base+'/stacks/'+info.stack);
  await page.getByRole('tab',{name:'Variables',exact:true}).click();
  await page.getByText('Add or update a reference',{exact:true}).click();
  await page.getByLabel('Logical secret name',{exact:true}).fill('password');
  await page.locator('form select[name="connection"]').selectOption('runtime-files');
  await page.getByLabel('Allowlisted local file alias',{exact:true}).fill('password');
  await page.getByRole('button',{name:'Save reference',exact:true}).click();
  await page.getByRole('tab',{name:'Variables',exact:true}).click();
  await page.getByText('Local alias · password',{exact:true}).waitFor();
  await page.locator('#plan-button').click();
  try {await page.getByRole('heading',{name:'Plan · source and secret preparation',exact:true}).waitFor({timeout:10000})} catch(error) {console.error('Plan visible diagnostic:',await page.locator('#plan-result').innerText());await page.screenshot({path:out+'-plan-failure.png',fullPage:true});throw error}
  assert.equal(await page.getByRole('tab',{name:'Plan',exact:true}).getAttribute('aria-selected'),'true');
  const prepared=page.waitForResponse(r=>r.url().endsWith('/broker/prepare'));
  await page.getByRole('button',{name:'Prepare references and Plan',exact:true}).click();
  assert.equal((await prepared).status(),200,'real preparation failed (body deliberately not logged)');
  await page.getByRole('button',{name:'Deploy reviewed Plan',exact:true}).waitFor();
  await page.getByText('/run/secrets/password',{exact:true}).first().waitFor();
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
  await page.screenshot({path:out+'-dark-390-review.png',fullPage:true});
  await page.getByRole('button',{name:'Deploy reviewed Plan',exact:true}).click();
  await page.getByText('Target reported succeeded;',{exact:false}).waitFor();
  await page.getByRole('tab',{name:'History',exact:true}).click();
  await page.locator('#stack-history-content').getByText('succeeded',{exact:true}).first().waitFor();
  await page.screenshot({path:out+'-dark-390-history.png',fullPage:true});
  await page.setViewportSize({width:1440,height:1100});
  await page.evaluate(()=>{localStorage.setItem('theme','light');document.documentElement.dataset.theme='light';document.documentElement.classList.remove('dark')});
  await page.screenshot({path:out+'-light-1440-history.png',fullPage:true});
  const status=await page.evaluate(()=>fetch('/fixture/finish',{method:'POST'}).then(r=>r.status));
  assert.equal(status,200,'real container/history evidence failed');
  console.log('PASS production Settings/Variables reference save/native Plan/confirmation/shared History + exact real Docker consumption; dark390/light1440');
 } finally {await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
