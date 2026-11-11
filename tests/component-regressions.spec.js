const {test,expect}=require('@playwright/test');
const baseURL=process.env.BRINE_FIXTURE_BASE_URL;
test.skip(!baseURL,'Read-only fixture required');

test('default text and semantic status palettes have readable contrast in both themes',async({page})=>{
  await page.goto(new URL('/settings',baseURL).href);
  for(const theme of ['dark','light']){
    await page.selectOption('#theme-select',theme);
    const ratios=await page.evaluate(()=>{
      const css=getComputedStyle(document.documentElement);
      const channels=name=>{const hex=css.getPropertyValue(name).trim();if(!/^#[0-9a-f]{6}$/i.test(hex))throw Error(name+': '+hex);return[1,3,5].map(i=>parseInt(hex.slice(i,i+2),16)/255);};
      const luminance=rgb=>rgb.map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4).reduce((s,v,i)=>s+v*[.2126,.7152,.0722][i],0);
      const contrast=(a,b)=>{const x=luminance(a),y=luminance(b);return(Math.max(x,y)+.05)/(Math.min(x,y)+.05)};
      const out={};
      for(const fg of ['--color-text','--color-muted','--color-dim'])for(const bg of ['--color-bg','--color-panel'])out[fg+' on '+bg]=contrast(channels(fg),channels(bg));
      for(const name of ['success','warning','error','info']){const fg=channels('--status-'+name),panel=channels('--color-panel'),mix=parseFloat(css.getPropertyValue('--status-fill'))/100;out[name+' badge']=contrast(fg,fg.map((v,i)=>v*mix+panel[i]*(1-mix)));}
      return out;
    });
    for(const[role,ratio]of Object.entries(ratios))expect(ratio,theme+' '+role).toBeGreaterThanOrEqual(4.5);
  }
});

test('editing the served theme file customizes every palette without regenerating components',async({page})=>{
  await page.route('**/static/theme.css',async route=>{
    const response=await route.fetch();
    const body=(await response.text()).replace(/--color-accent\s*:[^;]+;/,'--color-accent: #267bd2;').replace(/--radius-control\s*:[^;]+;/,'--radius-control: 11px;');
    await route.fulfill({response,body});
  });
  await page.goto(new URL('/settings',baseURL).href);
  for(const accent of ['sage','rose','glacier']){
    await page.locator(`label:has(input[name="brine-accent"][value="${accent}"])`).click();
    await expect(page.locator('.ui-button-primary').first()).toHaveCSS('background-color','rgb(38, 123, 210)');
    await expect(page.locator('.ui-button-primary').first()).toHaveCSS('border-radius','11px');
  }
});

test('editor typography and colors use the same live theme without replacing content',async({page})=>{
  await page.goto(new URL('/stacks/observability/edit',baseURL).href);
  await expect(page.locator('.cm-editor')).toBeVisible();
  const dimensions=await page.evaluate(()=>{
    const root=getComputedStyle(document.documentElement),body=getComputedStyle(document.body),editor=getComputedStyle(document.querySelector('.cm-editor'));
    return{root:parseFloat(root.fontSize),configuredRoot:parseFloat(root.getPropertyValue('--font-size-root')),body:body.fontSize,editor:editor.fontSize};
  });
  expect(dimensions.root).toBe(dimensions.configuredRoot);expect(dimensions.editor).toBe(dimensions.body);
  const text=await page.locator('.cm-content').innerText();
  await page.evaluate(()=>document.documentElement.style.setProperty('--editor-bg','#112233'));
  await expect(page.locator('.cm-editor')).toHaveCSS('background-color','rgb(17, 34, 51)');
  await expect.poll(()=>page.locator('.cm-content').innerText()).toBe(text);
});

test('toast duration is controlled by the theme and messages stay plain text',async({page})=>{
  await page.goto(baseURL);
  await page.evaluate(()=>{
    document.documentElement.style.setProperty('--toast-duration','500ms');
    showToast('<img src=x onerror="window.injected=true">','info');
  });
  await expect(page.locator('#toasts .ui-notice-info')).toContainText('<img');
  await expect(page.locator('#toasts img')).toHaveCount(0);
  expect(await page.evaluate(()=>window.injected)).toBeUndefined();
  await expect(page.locator('#toasts .ui-notice')).toHaveCount(0,{timeout:2000});
});
