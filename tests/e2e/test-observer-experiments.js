const {chromium}=require('playwright');
const path=require('path');
const root=path.resolve(__dirname,'../..');
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH || undefined,headless:true,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1280,height:900}});
 await page.route('http://comparison.test/**',route=>route.fulfill({contentType:'text/html',body:'<!DOCTYPE html><html><head></head><body><main id="app"></main></body></html>'}));
 await page.goto('http://comparison.test/#/compare?a=a&b=b');
 await page.addStyleTag({path:path.join(root,'public/style.css')});
 await page.evaluate(()=>{
 window.CLIENT_TTL={observers:0};window.escapeHtml=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 window.timeAgo=()=> 'recent';window.payloadTypeColor=()=> 'channel';
 window.registerPage=(_,page)=>{window.comparePage=page;};
 window.api=async url=>{
  if(url==='/observers')return {observers:[{id:'a',name:'Receiver A',iata:'SMF'},{id:'b',name:'Reference B',iata:'SMF'}]};
  const q=new URLSearchParams(url.split('?')[1]), t=Date.parse(q.get('since'))/1000;
  return {since:q.get('since'),until:q.get('until'),scanned:6,scan_cap:50000,observer_cap:10000,truncated:false,packets:[
   {observer_id:'a',hash:'aa',raw_hex:'aabb',path_json:'["aa"]',timestamp:t+10,snr:2,route_type:1,payload_type:5},
   {observer_id:'a',hash:'aa',raw_hex:'aacc',path_json:'["bb"]',timestamp:t+12,snr:4,route_type:1,payload_type:5},
   {observer_id:'b',hash:'aa',raw_hex:'aabb',path_json:'["aa"]',timestamp:t+10,snr:0,route_type:1,payload_type:5}]};
 };
 });
 await page.addScriptTag({path:path.join(root,'public/payload-labels.js')});
 await page.addScriptTag({path:path.join(root,'public/compare.js')});
 await page.evaluate(()=>window.comparePage.init(document.getElementById('app')));
 await page.getByText('Reception comparison',{exact:true}).waitFor();
 await page.screenshot({path:path.join(process.env.COMPARISON_QA_DIR || '/tmp','observer-comparison-desktop.png'),fullPage:true});
 await page.locator('#compareAt').fill('2026-11-11T10:00');
 await page.locator('#compareExperiment').check();
 await page.locator('#compareRefresh').click();
 await page.getByText('Before change',{exact:true}).waitFor();
 await page.locator('.compare-notes summary').click();
 await page.locator('#compareNote').fill('<img src=x onerror=alert(1)> Filter removed');
 await page.locator('#compareSaveNote').click();
 if(await page.locator('#compareNotes img').count())throw Error('unsafe note HTML');
 if((await page.url()).includes('Filter'))throw Error('private note leaked in URL');
 await page.setViewportSize({width:390,height:844});
 await page.screenshot({path:path.join(process.env.COMPARISON_QA_DIR || '/tmp','observer-comparison-mobile.png'),fullPage:true});
 const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth);
 if(overflow)throw Error('mobile overflow');
 await page.getByRole('button',{name:'Remove',exact:true}).click();
 if(await page.locator('#compareNotes button').count())throw Error('marker not removed');
 await browser.close();console.log('Browser desktop/mobile, before-after, private notes passed');
})().catch(e=>{console.error(e);process.exit(1)});
