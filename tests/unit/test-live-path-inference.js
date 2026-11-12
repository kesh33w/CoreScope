'use strict';
const fs = require('fs'), assert = require('assert'), vm = require('vm');
const root = require('path').resolve(__dirname, '../..');
const file = require('path').join(root, 'public/live.js');
const source = fs.readFileSync(file, 'utf8');
// Synthetic equivalent of a short-prefix collision across distant regions.
const fixture = {
  nodes:[
    {public_key:'BD01',name:'Far node',role:'repeater',lat:10,lon:10},
    {public_key:'C901',name:'Near node',role:'repeater',lat:40,lon:40},
  ],
  observers:[{id:'receiver',lat:40.01,lon:40.01}],
  observation:{id:99606,observer_id:'receiver',path_json:'["BD","C9"]',decoded_json:'{}',resolved_path:'["BD01","C901"]'},
  graph:{edges:[]},coords:{},
};
const snapshot = {nodes:{nodes:fixture.nodes}, observers:{observers:fixture.observers}, observations:[fixture.observation], graph:fixture.graph};
const observers = Object.fromEntries(fixture.observers.map(o => [o.id, o]));
const coords = fixture.coords;
const ctx = {window:{}, console, nodeData:Object.fromEntries(snapshot.nodes.nodes.map(n=>[n.public_key,n])), observerData:observers, observerCoords:coords, showGhostHops:true};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(root+'/public/hop-resolver.js','utf8'),ctx);
ctx.HopResolver=ctx.window.HopResolver;
ctx.HopResolver.init(snapshot.nodes.nodes,{observers:snapshot.observers.observers,iataCoords:coords});
ctx.HopResolver.setAffinity(snapshot.graph);
vm.runInContext(source.slice(source.indexOf('  function resolveHopPositions('), source.indexOf('  function animatePath(')),ctx);
const example=snapshot.observations.find(o=>o.id===99606);
const result=ctx.resolveHopPositions(JSON.parse(example.path_json),JSON.parse(example.decoded_json),JSON.parse(example.resolved_path||'null'),example.observer_id);
assert.strictEqual(result.length,2);
assert.strictEqual(result[0].known,false,'Distant BD must remain unknown');
assert.strictEqual(result[0].pos,null,'unknown BD must not get invented coordinates');
assert.strictEqual(result[1].known,true);
assert.strictEqual(result[1].name,'Near node');
assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.splitLocatedHopPositions(result))).map(x=>x.length),[1]);
const p=(key)=>({key,known:true,pos:[37,-122],name:key});
assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.splitLocatedHopPositions([p('A'),{known:false,pos:null},p('B'),p('C')]))).map(s=>s.map(p=>p.key)),[['A'],['B','C']]);
for(const ghost of [false,true]){
 ctx.showGhostHops=ghost;
 const a=ctx.resolveHopPositions(['AA','FF','BB'],{},null,'local');
 assert(a.every(p=>!p.ghost),'no interpolated unknown hop positions');
}
const original=JSON.stringify(example);
ctx.resolveHopPositions(JSON.parse(example.path_json),JSON.parse(example.decoded_json),JSON.parse(example.resolved_path||'null'),example.observer_id);
assert.strictEqual(JSON.stringify(example),original,'observation unchanged');
const nodes=[{public_key:'AA01',name:'A',role:'repeater',lat:37,lon:-122},{public_key:'BB01',name:'B',role:'repeater',lat:37.01,lon:-122},{public_key:'CC01',name:'C',role:'repeater',lat:37.02,lon:-122}];
ctx.observerData.local={lat:37.01,lon:-122};
ctx.HopResolver.init(nodes);
let r=ctx.resolveHopPositions(['AA','FF','BB','CC'],{},null,'local');
assert.deepStrictEqual(JSON.parse(JSON.stringify(ctx.splitLocatedHopPositions(r))).map(s=>s.map(p=>p.key)),[['AA01'],['BB01','CC01']]);
r=ctx.resolveHopPositions(['AA','AA'],{},null,'local');
assert.strictEqual(r.filter(p=>p.key==='AA01'&&p.known).length,1,'same node cannot occupy two path positions');
assert(ctx.resolveHopPositions(['AA'],{pubKey:'sender',lat:NaN,lon:-122},null).every(p=>p.key!=='sender'));
assert(ctx.resolveHopPositions(['AA'],{pubKey:'sender',lat:0,lon:0},null).every(p=>p.key!=='sender'));
assert.strictEqual(ctx.resolveHopPositions([],{pubKey:'sender',lat:37,lon:-122},null)[0].key,'sender');
console.log('PASS: distant prefix regression, ordered gaps, ghost setting, no mutation, repeated prefixes, valid origin');

// Exercise the production animator and clickable-path registration, not a copy.
const lines=[], clicks=[], pulses=[];
Object.assign(ctx,{animLayer:{},pathsLayer:{},nodesLayer:{},nodeMarkers:{},activeAnims:0,MAX_CONCURRENT_ANIMS:20,
  document:{getElementById(){return {textContent:''}}},
  pulseNode(key,pos){pulses.push(key)},drawAnimatedLine(a,b,color,done){lines.push([a,b]);done()},
  registerClickablePath(points){clicks.push(points)},
});
vm.runInContext(source.slice(source.indexOf('  function animatePath('),source.indexOf('  function pulseNode(')),ctx);
const point=(key,n)=>({key,pos:[n,n],name:key,known:true});
ctx.animatePath([point('A',1),{pos:null,known:false},point('B',2),point('C',3)],'TXT','blue','',null,{hash:'test',ts:0});
assert.deepStrictEqual(lines,[[[2,2],[3,3]]]);
assert.deepStrictEqual(JSON.parse(JSON.stringify(clicks)),[[[2,2],[3,3]]]);
assert(pulses.includes('A'));
assert.strictEqual(ctx.activeAnims,0);
console.log('PASS: animator and clickable paths never bridge unknown slots');

assert(ctx.resolveHopPositions(['AA','BB'],{},null,'missing-observer').every(p=>p.pos===null),'missing receiver must fail closed');
const sound=[];
ctx.animatePath([point('A',1),point('B',2),{pos:null},point('C',3),point('D',4)],'TXT','blue','',(i)=>sound.push(i),{hash:'split',ts:0});
assert.strictEqual(sound.length,0,'split paths must not restart audio synchronization');
console.log('PASS: missing observer fails closed; split paths do not restart audio');

(async()=>{
  ctx.api=async(path)=>path==='/observers'?{observers:[{id:'fresh',iata:'AAA'}]}:{coords:{AAA:{lat:37,lon:-122}}};
  ctx.setObserverIataMap=()=>{};
  ctx.buildObserverIataMap=data=>Object.fromEntries(data.observers.map(o=>[o.id,o.iata]));
  vm.runInContext(source.slice(source.indexOf('  async function loadLiveObservers('),source.indexOf('  let _affinityInterval')),ctx);
  await ctx.loadLiveObservers();
  ctx.initLiveHopResolver(nodes);
  assert(ctx.resolveHopPositions(['AA','BB'],{},null,'fresh').every(p=>p.known),'fresh observer IATA must anchor inference');
  ctx.api=async(path)=>{if(path==='/iata-coords')throw new Error('expected coordinate failure');return {observers:[{id:'fresh',iata:'AAA'}]}};
  await ctx.loadLiveObservers();
  ctx.initLiveHopResolver(nodes);
  assert(ctx.resolveHopPositions(['AA','BB'],{},null,'fresh').every(p=>p.known),'coordinate failure keeps last valid centers');
  ctx.api=async()=>{throw new Error('expected offline fixture')};
  ctx.console={warn(){}};
  await ctx.loadLiveObservers();
  assert(ctx.resolveHopPositions(['AA','BB'],{},null,'fresh').every(p=>p.known),'failed refresh keeps last valid roster');
  assert(ctx.resolveHopPositions(['AA','BB'],{},null,'missing').every(p=>!p.known));
  console.log('PASS: observer loading, IATA fallback, refresh failure, index rebuild');
})().catch(e=>{console.error(e);process.exitCode=1});

const dashed=[];
ctx.pathsLayer={hasLayer:()=>true,removeLayer(){}};
ctx.L={polyline(points){dashed.push(points);return{addTo(){return this}}},circleMarker(){return{addTo(){return this}}}};
ctx.getComputedStyle=()=>({getPropertyValue:()=>''});ctx.document.documentElement={};ctx.setTimeout=()=>{};
vm.runInContext(source.slice(source.indexOf('  function drawDashedPath('),source.indexOf('  function resolveHopPositions(')),ctx);
ctx.drawDashedPath([point('A',1),{pos:null},point('B',2),point('C',3)],'blue');
assert.deepStrictEqual(JSON.parse(JSON.stringify(dashed)),[[[2,2],[3,3]]]);
console.log('PASS: unreached TRACE segments do not bridge unknown hops');
