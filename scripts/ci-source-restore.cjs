// Exercise the packaged Brine/Dill pair on an ephemeral Linux CI runner.
// No Docker socket is ever passed to Brine. The synthetic target rejects and
// counts all non-read requests. Never run against a supplied host or workspace.
'use strict';
const {spawnSync}=require('node:child_process');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),http=require('node:http');
const assert=require('node:assert/strict');
const {randomBytes}=require('node:crypto');
const {chromium,expect}=require('@playwright/test');
function docker(...args){
 const cmd=spawnSync('docker',args,{encoding:'utf8'});
 if(cmd.status!==0)throw Error('Disposable fixture container command failed: '+cmd.stderr.slice(-1000));
 return cmd.stdout.trim();
}
async function main(){
 assert.equal(process.platform,'linux');assert.equal(process.env.GITHUB_ACTIONS,'true');
 process.umask(0o077);
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'brine-restore-ci-'));
 const local=n=>path.join(root,n);
 for(const dir of ['data','data/secret-state','archive','keys','engine']) fs.mkdirSync(local(dir),{mode:0o700});
 fs.writeFileSync(local('keys/source'),randomBytes(32),{mode:0o600,flag:'wx'});
 let reads=0,writes=0,refuseReads=false,cid,browser;
 const engine=http.createServer((req,res)=>{
  if(!['GET','HEAD'].includes(req.method)){writes++;res.writeHead(405).end();return}
  if(req.url==='/_ping'){res.setHeader('API-Version','1.47');res.end('OK');return}
  if(/^\/(v[\d.]+\/)?containers\/json\b/.test(req.url)){reads++;if(refuseReads){res.writeHead(503).end();return}}
  res.setHeader('Content-Type','application/json');res.end('[]');
 });
 await new Promise((resolve,reject)=>engine.once('error',reject).listen(local('engine/target.sock'),resolve));
 try{
  const mounts=['--user',process.getuid()+':'+process.getgid(),'-v',local('data')+':/data','-v',local('archive')+':/archive','-v',local('keys')+':/keys:ro','-v',local('engine')+':/engine:ro'];
  const flags=['--source-history-dir','/archive','--source-history-key','/keys/source','--engine-socket','/engine/target.sock'];
  docker('run','--rm',...mounts,'brine:ci',...flags,'--init-source-history');
  cid=docker('run','-d','-p','127.0.0.1::8080',...mounts,'brine:ci',...flags,'--bind','0.0.0.0','--auth-user','operator','--auth-password','synthetic-browser-only','--api-token','synthetic-api-only');
  const port=docker('port',cid,'8080/tcp');assert.match(port,/^127\.0\.0\.1:\d+$/);
  const base='http://'+port;await expect.poll(async()=>{try{return (await fetch(base+'/api/health')).status}catch{return 0}},{timeout:15000}).toBe(200);
  const api=(method,route,data)=>fetch(base+route,{method,headers:{Authorization:'Bearer synthetic-api-only','Content-Type':'application/json'},body:data===undefined?undefined:JSON.stringify(data)});
  const name='restore-demo',stack='/stacks/'+name,history='/web'+stack+'/history';
  const src='amends "package://pkg.ushd.ca/dill@0.8.1#/dill.pkl"\nconfig { name = "restore-demo"; engine = "docker" }\n// checkpoint-A\n';
  const slot=local('data/stacks/'+name+'/compose.pkl');
  assert.equal((await api('POST','/api/stacks/',{name,pkl_content:src})).status,201);
  const original=fs.readFileSync(slot,'utf8');
  browser=await chromium.launch();const page=await browser.newPage();
  const mutating=[];page.on('request',r=>{if(r.method()==='POST'&&/\/(up|down|start|stop|restart|teardown)$/.test(new URL(r.url()).pathname))mutating.push(r.method())});
  await page.goto(base+'/login?next='+stack);await page.locator('[name="user"]').fill('operator');await page.locator('[name="password"]').fill('synthetic-browser-only');await page.getByRole('button',{name:/sign in/i}).click();
  await page.goto(base+stack);
  const btn=(name)=>page.getByRole('button',{name,exact:true});
  const retain=async()=>{await btn('Review checkpoint').click();await btn('Retain source revision').click();await expect(page.getByText('Checkpoint retained',{exact:false}).first()).toBeVisible()};
  await btn('Restore').click();await retain();
  assert.equal(fs.readFileSync(slot,'utf8'),original);
  await page.goto(base+stack+'/edit');const editor=page.locator('.cm-content');
  await editor.click();await editor.press('Control+End');await page.keyboard.insertText('\n// edited-B');
  await btn('Update').click();await page.waitForURL(base+stack);
  assert.match(fs.readFileSync(slot,'utf8'),/edited-B/);
  await btn('Restore').click();await btn('Review restore from r1').click();
  await btn('Compare file contents').click();await page.getByText('Source excerpt',{exact:true}).click();
  await expect(page.locator('.source-excerpts')).toContainText('edited-B');
  const form=await page.locator('form[action$="/history/confirm"]').evaluate(f=>Object.fromEntries(new FormData(f)));
  const confirm=(fields)=>page.request.post(base+history+'/confirm',{form:fields,maxRedirects:0});
  assert.equal((await confirm({...form,_csrf:''})).status(),403);
  assert.equal((await api('POST',history+'/confirm',{})).status,403);
  const edited=fs.readFileSync(slot,'utf8')+'\n// edited-C';
  assert.equal((await api('PUT','/api'+stack,{pkl_content:edited})).status,200);
  assert.equal((await confirm(form)).status(),409);assert.equal(fs.readFileSync(slot,'utf8'),edited);
  await page.goto(base+stack+'?tab=history');await btn('Review restore from r1').click();
  refuseReads=true;
  const second=await page.locator('form[action$="/history/confirm"]').evaluate(f=>Object.fromEntries(new FormData(f)));
  assert.equal((await confirm(second)).status(),409);assert.equal(fs.readFileSync(slot,'utf8'),edited);
  refuseReads=false;
  fs.renameSync(local('data/secret-state'),local('data/held-secret-state'));
  assert.equal((await confirm(second)).status(),409);
  fs.renameSync(local('data/held-secret-state'),local('data/secret-state'));
  await btn('Restore as new revision').click();
  await expect(page.getByText('r3 ← r1',{exact:true})).toBeVisible();
  assert.equal(fs.readFileSync(slot,'utf8'),original);
  assert.equal((await confirm(second)).status(),409);
  await btn('Review restore from r2').click();await btn('Compare file contents').click();await page.getByText('Source excerpt',{exact:true}).click();
  await expect(page.locator('.source-excerpts')).toContainText('edited-C');
  assert.equal(fs.readdirSync(local('archive')).filter(n=>n.startsWith('source-object-')).length,3);
  assert.ok(reads>0);assert.equal(writes,0);assert.equal(mutating.length,0);
  console.log('Packaged source restore: checkpoint → edit → compare → r3 from r1; r2 preserves the edited bytes. Stale, missing-CSRF, bearer, unavailable target and missing protection-state submissions denied. Zero lifecycle requests and zero target writes.');
 }finally{
  try{if(browser)await browser.close();if(cid)docker('rm','-f',cid)}finally{await new Promise(resolve=>engine.close(resolve));fs.rmSync(root,{recursive:true,force:true})}
 }
}
main().catch(e=>{console.error(e.message);process.exitCode=1});
