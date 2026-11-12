const { test, expect } = require('@playwright/test');
const baseURL = process.env.BRINE_FIXTURE_BASE_URL;
for (const failRefresh of [false, true]) {
  test(`client-only: failed apply refreshes active references or marks unknown (${failRefresh})`, async ({ page }) => {
    let applied = false;
    await page.route('**/api/stacks/observability/secret-bindings**', async route => {
      const req = route.request(), path = new URL(req.url()).pathname;
      if (req.method() === 'GET') {
        if (applied && failRefresh) return route.fulfill({status:503,json:{error:'unavailable'}});
        return route.fulfill({status:200,json:{target:'lab-a',stack:'observability',draft:{bindings:[{name:'db',source:'prod',revision:'r2'}]},active:{bindings:[{name:'db',source:'prod',revision:applied?'r2':'r1'}]}}});
      }
      if (path.endsWith('/preview')) return route.fulfill({status:200,json:{preview_token:'public-token',plan:{changes:[],summary:{create:0,recreate:0,remove:0,noop:0}}}});
      if (path.endsWith('/confirm')) { applied = true; return route.fulfill({status:502,json:{error:'Activation or deployment failed; partial changes may have occurred.'}}); }
      return route.fulfill({status:200,json:{status:'ok'}});
    });
    page.on('dialog', dialog => dialog.accept());
    await page.goto(new URL('/stacks/observability', baseURL).href);
    await expect(page.locator('#secret-active')).toContainText('r1');
    await page.locator('#secret-preview').click(); await page.locator('#secret-confirm').click();
    await expect(page.locator('#secret-message')).toContainText('partial changes');
    await expect(page.locator('#secret-active')).toContainText(failRefresh ? 'unknown' : 'r2');
  });
}
