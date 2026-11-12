'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const source = fs.readFileSync(path.resolve(__dirname, '../../public/observer-detail.js'), 'utf8');

function renderObserver(id) {
  let page;
  const requests = [];
  const context = vm.createContext({
    window: {},
    document: {
      documentElement: {},
      getElementById: key => key === 'obsDaysSelect' ? { addEventListener() {} } : null,
      querySelector: () => null,
    },
    Chart: { defaults: {} },
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    registerPage: (name, handlers) => { if (name === 'observer-detail') page = handlers; },
    api: url => { requests.push(url); return new Promise(() => {}); },
  });
  vm.runInContext(source, context);
  const app = { innerHTML: '' };
  page.init(app, id);
  const match = app.innerHTML.match(/href="(#\/nodes\/[^\"]+)"/);
  assert.ok(match, 'actual observer page must render a node link');
  return { href: match[1], requests };
}

const key = 'AB'.repeat(32);
for (const suffix of ['@host-rf@SMF', '@bay@SFO']) {
  const id = key + suffix;
  const result = renderObserver(id);
  assert.strictEqual(result.href, '#/nodes/' + key.toLowerCase(), 'scoped feed must link to its real node key');
  assert.ok(result.requests.includes('/observers/' + encodeURIComponent(id)), 'observer request must keep the separate feed ID');
  assert.ok(result.requests.includes('/observers/' + encodeURIComponent(id) + '/analytics?days=7'), 'analytics must keep the separate feed ID');
}

for (const id of [key, 'legacy@named-observer', 'legacy@bay@SFO', key + '@bad@radio@SMF', key + '@@SMF']) {
  assert.strictEqual(renderObserver(id).href, '#/nodes/' + encodeURIComponent(id.toLowerCase()), 'legacy and malformed IDs must retain existing link behavior');
}
console.log('Observer radio-feed node links: 7 cases passed');
