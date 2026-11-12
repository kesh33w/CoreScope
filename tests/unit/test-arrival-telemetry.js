const assert = require('assert');
const fs = require('fs');
const vm = require('vm');
const path = require('path');
const source = fs.readFileSync(path.join(__dirname, '../../public/app.js'), 'utf8');
const start = source.indexOf('function telemetryArrivalHost(');
const end = source.indexOf('const telemetryDocumentArrivalHost', start);
const context = { URL, TextEncoder, TextDecoder };
vm.createContext(context);
vm.runInContext(source.slice(start, end), context);
for (const [input, expected] of [
  ['https://example.org/private?search=secret#private', 'example.org'],
  ['https://user:password@example.org/path', 'example.org'],
  ['', ''], ['not a URL', ''], ['javascript:alert(1)', ''],
  ['https://example.org:443/', 'example.org']
]) assert.strictEqual(context.telemetryArrivalHost(input), expected);
console.log('6 hostname-only arrival tests passed');

assert.strictEqual(context.telemetryBoundedURL('https://example.org/?q=radio#map').url, 'https://example.org/?q=radio#map');
const long = context.telemetryBoundedURL('https://example.org/?q=' + '🌍'.repeat(2000));
assert(long.truncated);
assert(Buffer.byteLength(long.url) <= 4096);
assert(!long.url.endsWith('�'));
assert.strictEqual(context.telemetryBoundedURL('javascript:alert(1)').url, '');
console.log('5 bounded full URL assertions passed');
