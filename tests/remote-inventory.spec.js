const {test, expect} = require('@playwright/test');
const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

test('duplicate inventory names expose distinct IDs accessibly', async ({page}) => {
  const fixture=path.join(os.tmpdir(),`brine-inventory-${process.pid}.html`);
  try {
    execFileSync('go',['test','./internal/remoteui','-run','^TestBrowserFixture$','-count=1'],{env:{...process.env,BRINE_REMOTE_FIXTURE:fixture,BRINE_REMOTE_INVENTORY:'1'}});
    await page.setContent(fs.readFileSync(fixture,'utf8'));
    const rows=page.locator('li');await expect(rows).toHaveCount(2);
    for (const [index,letter] of ['a','b'].entries()) {
      await expect(rows.nth(index).locator('code')).toHaveText(letter.repeat(12));
      await expect(rows.nth(index).locator('.sr-only')).toHaveText('Full container ID: '+letter.repeat(64));
      await expect(rows.nth(index).getByRole('link',{name:'Inspect same, full container ID '+letter.repeat(64)})).toHaveAttribute('href',new RegExp('container='+letter.repeat(64)));
    }
  } finally {if(fs.existsSync(fixture))fs.unlinkSync(fixture)}
});
