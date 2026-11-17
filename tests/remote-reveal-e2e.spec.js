const { test, expect } = require('@playwright/test');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawn, spawnSync } = require('child_process');

// This is a synthetic agent/signer and a fixture-only mount, not the production router or SSH.
test.describe.configure({ mode: 'serial' });
let dir, base, binary;
const children = [];
async function startFixture(shortTTL = false) {
  const subdir = fs.mkdtempSync(path.join(dir, 'server-'));
  const child = spawn(binary, ['-test.run=^TestRealRevealE2EFixture$', '-test.timeout=180s'], { env: {...process.env, BRINE_REAL_REVEAL_DIR: subdir, BRINE_REAL_REVEAL_SHORT_TTL: shortTTL ? '1' : '0'}, stdio: 'ignore' });
  children.push(child);
  let startupError = false;
  child.on('error', () => { startupError = true; });
  const address = path.join(subdir, 'address');
  await expect.poll(() => {
    if (startupError || child.exitCode !== null || child.signalCode !== null) throw new Error('fixture exited during startup');
    return fs.existsSync(address);
  }, { timeout: 15000 }).toBe(true);
  return fs.readFileSync(address, 'utf8');
}
test.beforeAll(async () => {
  test.setTimeout(120000);
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'brine-reveal-'));
  fs.chmodSync(dir, 0o700);
  binary = path.join(dir, 'fixture');
  const build = spawnSync('go', ['test', '-c', '-o', binary, './internal/remoteui'], { timeout: 90000, encoding: 'utf8' });
  if (build.status !== 0) throw new Error('fixture build failed or timed out');
  base = await startFixture();
});
test.afterAll(async () => {
  for (const child of children) if (child.exitCode === null && child.signalCode === null) {
    const exited = new Promise(resolve => child.once('exit', resolve));
    child.kill('SIGTERM');
    await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 3000))]);
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGKILL');
      await Promise.race([exited, new Promise((_, reject) => setTimeout(() => reject(new Error('fixture failed to stop')), 3000))]);
    }
  }
  if (dir) fs.rmSync(dir, { recursive: true, force: true });
});
async function login(page) {
  await page.goto(base + '/login');
  await page.getByLabel('Username').fill('admin');
  await page.getByLabel('Password').fill('fixture-password');
  await page.getByRole('button', { name: 'Sign in' }).click();
  return page.evaluate(async () => (await fetch('/csrf')).text());
}
async function review(page, csrf) {
  await page.goto(base + '/login');
  await page.evaluate(({csrf, id}) => {
    const form = document.createElement('form'); form.method='POST'; form.action='/remote/reveal/review';
    for (const [name,value] of Object.entries({_csrf:csrf, host:'server', container:id})) {
      const input=document.createElement('input'); input.name=name; input.value=value; form.appendChild(input);
    }
    document.body.appendChild(form); form.submit();
  }, {csrf, id:'a'.repeat(64)});
  await expect(page.getByRole('heading', {name:'Review environment reveal'})).toBeVisible();
}
test('real HTTP auth, reviewed one-shot approval, audit and clearing', async ({browser}) => {
  test.setTimeout(90000);
  const c1=await browser.newContext(), c2=await browser.newContext();
  try {
    const page=await c1.newPage(), other=await c2.newPage();
    const csrf=await login(page), csrf2=await login(other);
    const stats = async () => (await c1.request.get(base+'/fixture/stats')).json();
    const baseline = await stats();
    const denied=await c2.request.post(base+'/remote/reveal/review',{form:{host:'server',container:'a'.repeat(64)}});
    expect(denied.status()).toBe(403);
    const unauth=await browser.newContext();
    try {
      const no=await unauth.request.post(base+'/remote/reveal/review',{form:{host:'server',container:'a'.repeat(64)}});
      expect(no.ok()).toBeFalsy();
      const bearer=await unauth.request.post(base+'/remote/reveal/review',{headers:{Authorization:'Bearer fixture-bearer'},form:{host:'server',container:'a'.repeat(64)}});
      expect(bearer.ok()).toBeFalsy();
    } finally { await unauth.close(); }
    expect(await stats()).toEqual(baseline);
    await page.bringToFront();
    await review(page,csrf);
    await expect(page.locator('body')).toContainText('a'.repeat(64));
    await expect(page.locator('body')).toContainText('server');
    await expect(page.locator('body')).toContainText('host.example');
    await expect(page.locator('body')).toContainText('operator');
    const requestID = await page.locator('p').filter({hasText:'Request ID:'}).locator('code').textContent();
    expect(requestID).toMatch(/^[a-f0-9]{32}$/);
    const labels = await page.locator('dl dt').allTextContents();
    expect(labels).toEqual(['id','revision','host','port','user','key_path','host_public_key','host_key_sha256','agent_installation_id','engine_id','remote_socket','agent_public_key','agent_fingerprint','engine_kind','engine_socket','daemon_id','generation','reveal_key_fingerprint']);
    const expectedTarget=await (await c1.request.get(base+'/fixture/target')).json();
    const shown=await page.locator('dl dd code').allTextContents();
    expect(shown.map(value => JSON.parse(value.replaceAll('\\x20',' ')))).toEqual(labels.map(label => String(expectedTarget[label])));
    const token=await page.locator('script').evaluate(el => { const m=el.textContent.match(/token:\s*"([^"]+)"/); return m && m[1]; });
    expect(token).toBeTruthy();
    for (const form of [{token,confirm:'yes'}, {_csrf:'invalid',token,confirm:'yes'}]) {
      const rejected=await c1.request.post(base+'/remote/reveal/approve',{form});
      expect(rejected.status()).toBe(403);
      expect(await stats()).toEqual(baseline);
    }
    const bearerApproval=await c1.request.post(base+'/remote/reveal/approve',{headers:{Authorization:'Bearer fixture-bearer'},form:{_csrf:csrf,token,confirm:'yes'}});
    expect(bearerApproval.status()).toBe(403);
    const anonymous=await browser.newContext();
    try {
      const form={_csrf:csrf,token,confirm:'yes'};
      const missing=await anonymous.request.post(base+'/remote/reveal/approve',{form,maxRedirects:0});
      expect(missing.ok()).toBeFalsy();
      const bearer=await anonymous.request.post(base+'/remote/reveal/approve',{headers:{Authorization:'Bearer fixture-bearer'},form,maxRedirects:0});
      expect(bearer.ok()).toBeFalsy();
    } finally { await anonymous.close(); }
    expect(await stats()).toEqual(baseline);
    const cross=await c2.request.post(base+'/remote/reveal/approve',{form:{_csrf:csrf2,token,confirm:'yes'}});
    expect(cross.status()).toBe(403);
    expect(await stats()).toEqual(baseline);
    const responsePromise=page.waitForResponse(r => r.url().endsWith('/remote/reveal/approve'));
    await page.locator('#approve').click();
    const response=await responsePromise;
    expect(response.status()).toBe(200);
    expect(response.headers()['x-fixture-audit-before-write']).toBe('verified');
    expect(await (await c1.request.get(base+'/fixture/request-id')).text()).toBe(requestID);
    const successStats=await stats();
    expect(successStats).toEqual({opens:baseline.opens+1,calls:baseline.calls+1,audit:baseline.audit+1,outcomes:[...baseline.outcomes,'succeeded'],cancelled:baseline.cancelled});
    const replay=await c1.request.post(base+'/remote/reveal/approve',{form:{_csrf:csrf,token,confirm:'yes'}});
    expect(replay.status()).toBe(403);
    expect(await stats()).toEqual(successStats);
    await expect.poll(async () => (await page.locator('#values').textContent()).includes('secret-value')).toBe(true);
    const result=JSON.parse(await page.locator('#values').textContent());
    expect(result.containerId === 'a'.repeat(64) && result.env.length === 1 && result.env[0].key === 'KEY' && result.env[0].value === 'secret-value').toBe(true);
    // Use real elapsed time: this session remains valid well beyond the 30s
    // independent display limit. Keeping this page foreground avoids a
    // visibilitychange clearing values for a different reason.
    const displayedAt=Date.now();
    await expect(page.locator('#values')).toBeEmpty({timeout:35000});
    const elapsed=Date.now()-displayedAt;
    expect(elapsed).toBeGreaterThanOrEqual(28000);
    expect((await c1.request.get(base+'/remote/session',{maxRedirects:0})).status()).toBe(200);
    // Reveal again so logout clearing is independently tested with live values.
    await review(page,csrf);
    await page.locator('#approve').click();
    await expect.poll(async () => (await page.locator('#values').textContent()).includes('secret-value')).toBe(true);
    await page.evaluate(async csrf => { await fetch('/logout',{method:'POST',body:new URLSearchParams({_csrf:csrf})}); },csrf);
    await expect(page.locator('#values')).toBeEmpty({timeout:5000});
    // Pending approvals become unusable on actual logout, without an agent call or audit.
    await other.bringToFront(); await review(other,csrf2);
    const pending=await other.locator('script').evaluate(el => el.textContent.match(/token:\s*"([^"]+)"/)[1]);
    const beforeLogout=await (await c2.request.get(base+'/fixture/stats')).json();
    await other.evaluate(async csrf => { await fetch('/logout',{method:'POST',body:new URLSearchParams({_csrf:csrf})}); },csrf2);
    const afterLogout=await c2.request.post(base+'/remote/reveal/approve',{form:{_csrf:csrf2,token:pending,confirm:'yes'}});
    expect(afterLogout.ok()).toBeFalsy();
    const c3=await browser.newContext();
    try {
      const p=await c3.newPage(); const c=await login(p);
      const getStats=async () => (await c3.request.get(base+'/fixture/stats')).json();
      expect(await getStats()).toEqual(beforeLogout);
      await review(p,c);
      const tok=await p.locator('script').evaluate(el => el.textContent.match(/token:\s*"([^"]+)"/)[1]);
      expect((await c3.request.post(base+'/fixture/failure',{form:{_csrf:c}})).ok()).toBe(true);
      const failed=await c3.request.post(base+'/remote/reveal/approve',{form:{_csrf:c,token:tok,confirm:'yes'}});
      expect(failed.ok()).toBeFalsy();
      expect((await failed.text()).includes('secret-value')).toBe(false);
      const failedStats=await getStats();
      expect(failedStats).toEqual({opens:beforeLogout.opens+1,calls:beforeLogout.calls+1,audit:beforeLogout.audit+1,outcomes:[...beforeLogout.outcomes,'failed'],cancelled:beforeLogout.cancelled});
      await review(p,c);
      const stale=await p.locator('script').evaluate(el => el.textContent.match(/token:\s*"([^"]+)"/)[1]);
      expect((await c3.request.post(base+'/fixture/mutate',{form:{_csrf:c}})).ok()).toBe(true);
      const rejected=await c3.request.post(base+'/remote/reveal/approve',{form:{_csrf:c,token:stale,confirm:'yes'}});
      expect(rejected.ok()).toBeFalsy();
      expect((await rejected.text()).includes('secret-value')).toBe(false);
      expect(await getStats()).toEqual(failedStats);
    } finally { await c3.close(); }
    const originalBase=base; base=await startFixture(true);
    const exp=await browser.newContext();
    try {
      const p=await exp.newPage(); const c=await login(p); await review(p,c);
      await p.locator('#approve').click();
      await expect.poll(async () => (await p.locator('#values').textContent()).includes('secret-value')).toBe(true);
      await expect(p.locator('#values')).toBeEmpty({timeout:10000});
      // A separate pending review expires without ever reaching the agent.
      const pendingContext=await browser.newContext();
      try {
        const q=await pendingContext.newPage(), csrfPending=await login(q); await review(q,csrfPending);
        const expiryToken=await q.locator('script').evaluate(el => el.textContent.match(/token:\s*"([^"]+)"/)[1]);
        const beforeExpiry=await (await pendingContext.request.get(base+'/fixture/stats')).json();
        await expect.poll(async () => (await pendingContext.request.get(base+'/remote/session',{maxRedirects:0})).status(), {timeout:11000}).not.toBe(200);
        const expired=await pendingContext.request.post(base+'/remote/reveal/approve',{form:{_csrf:csrfPending,token:expiryToken,confirm:'yes'},maxRedirects:0});
        expect(expired.ok()).toBeFalsy();
        const verify=await browser.newContext();
        try { const v=await verify.newPage(); await login(v); expect(await (await verify.request.get(base+'/fixture/stats')).json()).toEqual(beforeExpiry); } finally { await verify.close(); }
      } finally { await pendingContext.close(); }
      // A blocked synthetic agent observes real session expiry and releases.
      const blocked=await browser.newContext(), observer=await browser.newContext();
      try {
        const b=await blocked.newPage(), o=await observer.newPage();
        const bc=await login(b); await login(o); await b.bringToFront(); await review(b,bc);
        const bt=await b.locator('script').evaluate(el => el.textContent.match(/token:\s*"([^"]+)"/)[1]);
        expect((await blocked.request.post(base+'/fixture/block',{form:{_csrf:bc}})).ok()).toBe(true);
        const observe=async () => {
          let r=await observer.request.get(base+'/fixture/stats',{maxRedirects:0});
          if (r.status() !== 200) {
            await observer.request.post(base+'/login',{form:{user:'admin',password:'fixture-password'},maxRedirects:0});
            r=await observer.request.get(base+'/fixture/stats',{maxRedirects:0});
          }
          return r.json();
        };
        const before=await observe();
        const inFlight=blocked.request.post(base+'/remote/reveal/approve',{form:{_csrf:bc,token:bt,confirm:'yes'},timeout:15000}).then(async r => ({ok:r.ok(),contains:(await r.text()).includes('secret-value')})).catch(() => ({ok:false,contains:false}));
        await expect.poll(async () => (await observe()).calls,{timeout:5000}).toBe(before.calls+1);
        await expect.poll(async () => (await observe()).cancelled,{timeout:11000}).toBe(before.cancelled+1);
        const result=await inFlight;
        expect(result.ok).toBe(false); expect(result.contains).toBe(false);
        const after=await observe();
        expect(after.opens).toBe(before.opens+1); expect(after.audit).toBe(before.audit+1);
        expect(after.outcomes[after.outcomes.length-1]).toBe('');
      } finally { await blocked.close(); await observer.close(); }
    } finally { await exp.close(); base=originalBase; }
  } finally { await c1.close(); await c2.close(); }
});
