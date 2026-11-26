const { test, expect } = require('@playwright/test');
const base=process.env.BRINE_FIXTURE_BASE_URL;
for(const width of [320,390,768,1440])test('stack sections, dates and actions fit '+width+'px', async({page})=>{
 await page.setViewportSize({width,height:844});await page.goto(base+'/stacks/homepage');
 const stamp=page.locator('.stack-overview time').first();
 await expect(stamp).toHaveText(/ago|in |just now/);await stamp.focus();await expect(page.locator('.exact-time').first()).toBeVisible();
 await expect(stamp).toHaveAttribute('title',/UTC/);await stamp.blur();await expect(page.locator('.exact-time').first()).toBeHidden();
 await page.getByRole('tab',{name:'Variables'}).click();await expect(page.locator('#stack-variables-content')).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
 await expect(page.getByRole('button',{name:'Restore',exact:true}).locator('svg')).toBeVisible();
});
