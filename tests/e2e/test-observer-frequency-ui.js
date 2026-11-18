const { chromium } = require('playwright');
const fs=require('fs');
(async()=>{
 const b=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_EXECUTABLE || undefined});const p=await b.newPage();let puts=0;let saved;
 await p.route('http://frequency.test/**',async r=>{
 const url=r.request().url();
 if(url.endsWith('/api/config/observer-frequency')) {
  if(r.request().method()==='PUT'){puts++;saved=r.request().postDataJSON();await new Promise(x=>setTimeout(x,100));return r.fulfill({contentType:'application/json',body:JSON.stringify({rules:saved.rules,restartRequired:true})});}
  return r.fulfill({contentType:'application/json',body:JSON.stringify({rules:{SFO:{mhz:910.525}},writeEnabled:true,restartRequired:true})});
 }
 if(url.endsWith('/style.css')) return r.fulfill({contentType:'text/css',body:fs.readFileSync('public/style.css','utf8')});
 if(url.endsWith('/customize.js'))return r.fulfill({contentType:'text/javascript',body:fs.readFileSync('public/customize-v2.js','utf8')});
 return r.fulfill({contentType:'text/html',body:'<html><head><meta charset="utf-8"><link rel="stylesheet" href="/style.css"></head><body><button id="customizeToggle">Customize</button><script src="/customize.js"></script></body></html>'});
 });
 await p.goto('http://frequency.test/#/?customizer=frequency');
 await p.waitForFunction(()=>!document.querySelector('#cv2-freq-fields').disabled);
 const rows=p.locator('.cv2-freq-rule');
 if(await rows.count()!==1)throw Error('saved SFO missing');
 await p.locator('#cv2-freq-save').click();
 if(!(await p.locator('#cv2-freq-status').textContent()).includes('API key'))throw Error('missing auth not caught');
 await p.locator('#cv2-freq-add').click();
 await rows.nth(1).locator('[data-field="region"]').fill('sfo');
 await rows.nth(1).locator('[data-field="mhz"]').fill('909.875');
 await p.locator('#cv2-freq-save').click();
 if(!(await p.locator('#cv2-freq-status').textContent()).includes('only once'))throw Error('duplicate accepted');
 await rows.nth(1).locator('[data-field="region"]').fill('smf');
 await rows.nth(1).locator('[data-field="maxStatusAgeSec"]').fill('1.5');
 await p.locator('#cv2-freq-save').click();
 if(!(await p.locator('#cv2-freq-status').textContent()).includes('valid frequency'))throw Error('fractional age accepted');
 await rows.nth(1).locator('[data-field="maxStatusAgeSec"]').fill('1200');
 await rows.nth(1).locator('[data-field="toleranceKHz"]').fill('0.25');
 await rows.nth(1).locator('[data-field="dropUnknown"]').check();
 await p.locator('#cv2-freq-key').fill('test-key');
 if(puts!==0)throw Error('draft was saved prematurely');
 await p.locator('#cv2-freq-save').click();
 await p.waitForFunction(()=>document.querySelector('#cv2-freq-status').textContent.startsWith('Saved.'));
 if(puts!==1||saved.rules.SFO.mhz!==910.525||saved.rules.SMF.mhz!==909.875||!saved.rules.SMF.dropUnknown||saved.rules.SMF.maxStatusAgeSec!==1200||saved.rules.SMF.toleranceKHz!==0.25)throw Error('save failed');
 if (process.env.SCREENSHOT_PATH) await p.screenshot({path:process.env.SCREENSHOT_PATH});
 await rows.nth(0).locator('.cv2-freq-remove').click();
 await p.locator('#cv2-freq-reset').click();
 if(await rows.count()!==2||puts!==1)throw Error('discard changed saved rules');
 await rows.nth(0).locator('.cv2-freq-remove').click();
 await p.locator('#cv2-freq-key').fill('test-key');await p.locator('#cv2-freq-save').click();
 await p.waitForFunction(()=>document.querySelector('#cv2-freq-status').textContent.startsWith('Saved.'));
 if(puts!==2||Object.keys(saved.rules).length!==1||!saved.rules.SMF)throw Error('remove failed');
 await rows.nth(0).locator('.cv2-freq-remove').click();
 await p.locator('#cv2-freq-key').fill('test-key');await p.locator('#cv2-freq-save').click();
 await p.waitForFunction(()=>document.querySelector('#cv2-freq-status').textContent.startsWith('Saved.'));
 if(puts!==3||Object.keys(saved.rules).length!==0)throw Error('disable all failed');
 await p.locator('.cust-close').click();if((await p.url()).includes('customizer='))throw Error('close URL retained');
 console.log('Browser: load, auth, validation, multiple rules, preserve SFO, discard, remove, disable all, restart notice, close passed');await b.close();
})().catch(e=>{console.error(e);process.exit(1)});
