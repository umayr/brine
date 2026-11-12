const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
test('reference draft, reviewed preview, and confirmation use only structured metadata', async ({ page }) => {
  let draft = null, active = null, preview = null; const calls = [];
  await page.route('**/api/stacks/observability/secret-bindings**', async route => {
    const req = route.request(), url = new URL(req.url()); calls.push([req.method(), url.pathname, req.postData()]);
    let body = {};
    if (url.pathname.endsWith('/status')) body = {status:'available'};
    else if (req.method() === 'GET') body = {target:'lab-a', stack:'observability', draft, active};
    else if (req.method() === 'PUT') { draft = req.postDataJSON(); preview = null; body = {status:'draft-saved'}; }
    else if (url.pathname.endsWith('/preview')) { preview = 'public-preview-token'; body = {preview_token:preview, plan:{changes:[{service:'web',kind:'recreate',diffs:[]}],summary:{create:0,recreate:1,remove:0,noop:0}}}; }
    else if (url.pathname.endsWith('/confirm')) { if (req.postDataJSON().preview_token !== preview) return route.fulfill({status:409, json:{error:'Preview is stale; preview again.'}}); active = draft; preview = null; body = {status:'applied'}; }
    await route.fulfill({status:200, json:body});
  });
  page.on('dialog', dialog => dialog.accept());
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await expect(page.locator('#secret-editor')).toBeVisible();
  await page.locator('#secret-add').click();
  await page.getByLabel('logical name').fill('db-password');
  await page.getByLabel('opaque source ID').fill('db-prod');
  await page.getByLabel('revision').fill('r2');
  await page.locator('#secret-save').click();
  await expect(page.locator('#secret-message')).toContainText('Draft saved');
  expect(active).toBeNull();
  await page.locator('#secret-status').click();
  await expect(page.locator('#secret-message')).toContainText('available now');
  await page.locator('#secret-preview').click();
  await expect(page.locator('#secret-plan')).toContainText('recreate');
  expect(active).toBeNull();
  await page.locator('#secret-confirm').click();
  await expect(page.locator('#secret-message')).toContainText('Approved references applied');
  expect(active.bindings).toEqual([{name:'db-password',source:'db-prod',revision:'r2'}]);
  await page.locator('#secret-active-status').click();
  await expect(page.locator('#secret-message')).toContainText('Active sources available');
  expect(calls.find(c => c[0] === 'PUT')[2]).not.toContain('value');
  expect(calls.find(c => c[1].endsWith('/confirm'))[2]).not.toContain('proof');
});

test('editing a preview invalidates confirmation', async ({ page }) => {
  await page.route('**/api/stacks/observability/secret-bindings**', async route => {
    const path = new URL(route.request().url()).pathname;
    const data = route.request().method() === 'GET' ? {target:'lab-a',stack:'observability',draft:{bindings:[{name:'db',source:'prod',revision:'r1'}]},active:null} : path.endsWith('/preview') ? {preview_token:'public-token',plan:{changes:[],summary:{create:0,recreate:0,remove:0,noop:0}}} : {status:'ok'};
    await route.fulfill({status:200,json:data});
  });
  await page.goto(new URL('/stacks/observability', baseURL).href);
  await expect(page.locator('#secret-editor')).toBeVisible();
  await page.locator('#secret-preview').click();
  await expect(page.locator('#secret-confirm')).toBeEnabled();
  await page.getByLabel('revision').fill('r2');
  await expect(page.locator('#secret-confirm')).toBeDisabled();
});
