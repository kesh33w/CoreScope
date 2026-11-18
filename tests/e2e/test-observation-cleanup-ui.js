'use strict';
const {chromium}=require('playwright');
const fs=require('fs');const assert=require('assert');
(async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.CHROMIUM_EXECUTABLE||undefined});
 const p=await browser.newPage();let submissions=0,previews=0,restores=0,body,enabled=true,delay=0;
 const errors=[];p.on('pageerror',e=>errors.push(e.message));
 await p.route('http://cleanup.test/**',async route=>{
 const req=route.request(),url=new URL(req.url());
 const reply=data=>route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
 if(url.pathname.endsWith('/capabilities'))return reply({writeEnabled:enabled,frequencyHistoryAvailable:false});
 if(url.pathname.endsWith('/preview')){previews++;const selection=req.postDataJSON();await new Promise(r=>setTimeout(r,delay));return reply({selection,count:3456,maxObservationId:9999});}
 if(url.pathname.endsWith('/status'))return reply({status:'done',deleted:3456,restartRequired:true});
 if(url.pathname.endsWith('/restore')){restores++;return reply({requestId:'def456'});}
 if(url.pathname==='/api/admin/observation-cleanup'){submissions++;body=req.postDataJSON();await new Promise(r=>setTimeout(r,100));return reply({requestId:'abc123'});}
 if(url.pathname==='/style.css')return route.fulfill({contentType:'text/css',body:fs.readFileSync('public/style.css','utf8')});
 if(url.pathname==='/cleanup.js')return route.fulfill({contentType:'text/javascript',body:fs.readFileSync('public/observation-cleanup.js','utf8')});
 if(url.pathname==='/customize.js')return route.fulfill({contentType:'text/javascript',body:fs.readFileSync('public/customize-v2.js','utf8')});
 return route.fulfill({contentType:'text/html',body:'<html><head><link rel="stylesheet" href="/style.css"></head><body><button id="customizeToggle">Customize</button><script src="/cleanup.js"></script><script src="/customize.js"></script></body></html>'});
 });
 await p.goto('http://cleanup.test/#/home?customizer=cleanup');await p.locator('#cleanup-observer').waitFor();await p.waitForFunction(()=>!document.querySelector('#cleanup-fields').disabled);
 await p.locator('#cleanup-observer').fill('ABC');await p.locator('#cleanup-iata').fill('SMF');await p.locator('#cleanup-preview').click();assert((await p.locator('#cleanup-status').textContent()).includes('API key'));assert.equal(previews,0);
 await p.locator('#cleanup-key').fill('example-key');await p.locator('#cleanup-preview').click();await p.locator('#cleanup-delete').waitFor();assert((await p.locator('#cleanup-summary').textContent()).includes('3,456'));
 p.once('dialog',d=>d.dismiss());await p.locator('#cleanup-delete').click();assert.equal(submissions,0);
 // Editing clears the old snapshot. A slow response must not resurrect it.
 delay=120;await p.locator('#cleanup-preview').click();await p.locator('#cleanup-observer').fill('XYZ');await p.waitForTimeout(200);assert(await p.locator('#cleanup-delete').isHidden());
 delay=0;await p.locator('#cleanup-preview').click();await p.locator('#cleanup-delete').waitFor();p.once('dialog',d=>d.accept());await p.locator('#cleanup-delete').click();
 // Repeat clicks during POST must not enqueue twice.
 await p.evaluate(()=>document.querySelector('#cleanup-delete').click());await p.waitForFunction(()=>document.querySelector('#cleanup-status').textContent.startsWith('Completed.'));
 assert.equal(submissions,1);assert.equal(body.selection.observerId,'XYZ');assert.equal(body.expectedCount,3456);assert.equal(body.maxObservationId,9999);assert((await p.locator('#cleanup-status').textContent()).includes('Restart all CoreScope servers'));
 assert(p.url().includes('cleanup_job=abc123'));p.once('dialog',d=>d.accept());await p.locator('#cleanup-restore').click();await p.waitForFunction(()=>document.querySelector('#cleanup-status').textContent.startsWith('Completed.'));assert.equal(restores,1);
 await p.locator('.cust-close').click();assert(!p.url().includes('customizer='));assert.equal(await p.locator('#cleanup-key').inputValue(),'');
 await p.locator('#customizeToggle').click();assert(p.url().includes('customizer=cleanup'));assert.equal(await p.locator('#cleanup-observer').inputValue(),'XYZ');assert.equal(await p.locator('#cleanup-key').inputValue(),'');
 await p.locator('.cust-close').click();enabled=false;await p.locator('#customizeToggle').click();await p.waitForFunction(()=>document.querySelector('#cleanup-status').textContent.includes('unavailable'));assert(await p.locator('#cleanup-fields').evaluate(e=>e.disabled));
 // Closing while a POST is pending must retain the result ID for recovery.
 enabled=true;await p.locator('.cust-close').click();await p.locator('#customizeToggle').click();await p.waitForFunction(()=>!document.querySelector('#cleanup-fields').disabled);
 await p.locator('#cleanup-key').fill('example-key');await p.locator('#cleanup-preview').click();await p.locator('#cleanup-delete').waitFor();p.once('dialog',d=>d.accept());await p.locator('#cleanup-delete').click();await p.locator('.cust-close').click();await p.waitForTimeout(180);
 assert.equal(await p.evaluate(()=>localStorage.getItem('corescope-cleanup-last-job')),'abc123');
 await p.evaluate(()=>location.hash='#/home?customizer=cleanup');await p.locator('#cleanup-observer').waitFor();assert.equal(await p.locator('#cleanup-restore-id').inputValue(),'abc123');
 await p.evaluate(()=>location.hash='#/home');await p.waitForFunction(()=>document.querySelector('.cust-overlay').classList.contains('hidden'));
 assert.deepStrictEqual(errors,[]);if(process.env.SCREENSHOT_PATH)await p.screenshot({path:process.env.SCREENSHOT_PATH});
 await browser.close();console.log('cleanup browser: auth, 3456-row snapshot, stale preview, cancel, repeat click, restore, restart notice, close/reopen, unavailable passed');
})().catch(e=>{console.error(e);process.exit(1)});
