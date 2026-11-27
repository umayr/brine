// Authorized rebuilt sandbox only. Credentials are read from a private file;
// never put them, provider bytes, cookies or response bodies in diagnostics.
const fs=require('node:fs');const assert=require('node:assert/strict');
const {chromium}=require(process.env.BRINE_BROKER_BROWSER_MODULE);
(async()=>{const [base,credentials,out,connection,providerOrigin]=process.argv.slice(2);assert.ok(connection&&providerOrigin,'explicit operator connection and HTTPS provider origin required');assert.equal(new URL(providerOrigin).protocol,'https:');const auth=JSON.parse(fs.readFileSync(credentials));const browser=await chromium.launch({headless:true});try{
 const context=await browser.newContext({viewport:{width:1440,height:1100}});
 const login=await context.request.post(base+'/login',{maxRedirects:0,form:{user:auth.user,password:auth.password}});assert.equal(login.status(),303,'authenticated sandbox login failed');
 const page=await context.newPage();page.setDefaultTimeout(45000);
 await page.goto(base+'/settings/secrets');await page.getByRole('heading',{name:connection,exact:true}).waitFor();await page.getByText(providerOrigin,{exact:true}).waitFor();
 await page.goto(base+'/stacks/infisical-demo');await page.getByRole('tab',{name:'Variables',exact:true}).click();await page.getByText('Add or update a reference',{exact:true}).click();
 await page.getByLabel('Logical secret name',{exact:true}).fill('password');await page.locator('form select[name="connection"]').selectOption(connection);await page.getByLabel('Infisical folder',{exact:true}).fill('/');await page.getByLabel('Infisical key',{exact:true}).fill('BRINE_RUNTIME_PASSWORD');await page.getByRole('button',{name:'Save reference',exact:true}).click();
 await page.locator('#plan-button').click();await page.getByRole('heading',{name:'Plan · source and secret preparation',exact:true}).waitFor();
 const prepared=page.waitForResponse(r=>r.url().endsWith('/broker/prepare'));
 await page.getByRole('button',{name:'Prepare references and Plan',exact:true}).click();assert.equal((await prepared).status(),200,'real Infisical preparation failed; response not logged');
 await page.getByRole('button',{name:'Deploy reviewed Plan',exact:true}).waitFor();await page.getByText('/run/secrets/password',{exact:true}).first().waitFor();await page.screenshot({path:out+'-review.png',fullPage:true});
 await page.getByRole('button',{name:'Deploy reviewed Plan',exact:true}).click();await page.getByText('Target reported succeeded;',{exact:false}).waitFor();await page.getByRole('tab',{name:'History',exact:true}).click();await page.locator('#stack-history-content').getByText('succeeded',{exact:true}).first().waitFor();await page.screenshot({path:out+'-history.png',fullPage:true});
 console.log('PASS rebuilt sandbox authenticated UI -> separately hosted Infisical TLS -> native reviewed Docker deployment -> shared successful History');
 }finally{await browser.close()}})().catch(error=>{console.error(error);process.exitCode=1});
