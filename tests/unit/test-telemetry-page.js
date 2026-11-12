'use strict';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const path = require('path');
const source = fs.readFileSync(path.join(__dirname, '../../public/app.js'), 'utf8');
const start = source.indexOf('function telemetryPageId(');
const end = source.indexOf('function connectWS()', start);
assert(start >= 0 && end > start);
const context = { location: {hash: '#/live', href: 'https://scope.example/#/live'}, document: {referrer: ''}, JSON, URL, TextEncoder, TextDecoder };
vm.createContext(context);
vm.runInContext(source.slice(start, end), context);
for (const [hash, expected] of [
 ['', 'packets'], ['#/live?private=secret', 'live'], ['#/observers/secret', 'observer-detail'],
 ['#/nodes/pubkey/analytics?tab=secret', 'node-analytics'], ['#/nodes/pubkey/reach', 'node-reach'],
 ['#/nodes/pubkey', 'nodes'], ['#/packet/secret', 'packet-detail'], ['#/tools/trace/secret','traces'],
 ['#/tools/path-inspector?path=secret','path-inspector'], ['#/roles','analytics'],
 ['#/unrecognized-secret', 'unknown'], ['#/account?token=secret','account'],
]) assert.strictEqual(context.telemetryPageId(hash), expected);
let sent=[];
const sock={readyState:1,send(x){sent.push(JSON.parse(x));}};
context.reportCurrentPage(sock);
context.reportCurrentPage(sock);
assert.strictEqual(sent.length,1);
context.location.hash='#/nodes/private?query=secret';
context.reportCurrentPage(sock);
assert.strictEqual(sent[1].page, 'nodes');
assert.strictEqual(sent[1].page_url, 'https://scope.example/#/live');
assert(!JSON.stringify(sent).includes('private'));
context.location.hash='#/unknown-private';
context.reportCurrentPage(sock);
assert.strictEqual(sent[2].page,'unknown');
context.reportCurrentPage(null);
context.reportCurrentPage({readyState:0, send(){throw new Error('connecting');}});
context.reportCurrentPage({readyState:1, send(){throw new Error('closed');}});
const next={readyState:1,send(x){sent.push(JSON.parse(x));}};
context.reportCurrentPage(next);
assert.strictEqual(sent.length,4,'new connection must report even same page');
assert(source.includes('reportCurrentPage(sock);'), 'socket open wired');
assert(source.includes('currentPage = basePage;\n  reportCurrentPage(ws);'), 'navigation wired');
console.log('page telemetry bounded metadata/navigation tests passed');
