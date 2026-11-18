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
 return r.fulfill({contentType:'text/html',body:'<html><head><link rel="stylesheet" href="/style.css"></head><body><button id="customizeToggle">Customize</button><script src="/customize.js"></script></body></html>'});
 });
 await p.goto('http://frequency.test/#/?customizer=frequency');
 await p.locator('#cv2-freq-enabled').waitFor();await p.waitForFunction(()=>!document.querySelector('#cv2-freq-fields').disabled);
 await p.locator('#cv2-freq-enabled').check();await p.locator('#cv2-freq-save').click();
 if(!(await p.locator('#cv2-freq-status').textContent()).includes('API key'))throw Error('missing auth not caught');
 await p.locator('#cv2-freq-key').fill('test-key');await p.locator('#cv2-freq-unknown').check();await p.locator('#cv2-freq-save').click();
 await p.waitForFunction(()=>document.querySelector('#cv2-freq-status').textContent.startsWith('Saved.'));
 if(puts!==1||!saved.rules.SFO||saved.rules.SMF.mhz!==909.875||!saved.rules.SMF.dropUnknown)throw Error('save failed');
 if (process.env.SCREENSHOT_PATH) await p.screenshot({path:process.env.SCREENSHOT_PATH});
 await p.locator('.cust-close').click();if((await p.url()).includes('customizer='))throw Error('close URL retained');
 console.log('Browser: initial load, no-auth rejection, save, preserve SFO, restart notice, close passed');await b.close();
})().catch(e=>{console.error(e);process.exit(1)});
