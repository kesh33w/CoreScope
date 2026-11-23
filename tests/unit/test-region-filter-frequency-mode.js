/**
 * Region filter frequency mode (config regionFilter.mode "frequency"). The
 * server lists frequencies in /api/config/regions and says so with
 * mode: "frequency" in /api/config/region-quick-picks. The filter then
 * labels itself "Frequency", and RegionFilter.observerKey(o) returns an
 * observer's frequency (from radio, rounded to kHz like the server) instead
 * of its IATA code, for the pages that match observers client-side.
 */
'use strict';
const REPO_ROOT = require('node:path').resolve(__dirname, '..', '..');
const vm = require('node:vm');
const fs = require('node:fs');
const assert = require('node:assert');

let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log(`  ✓ ${name}`); }
  catch (e) { failed++; console.error(`  ✗ ${name}: ${e.message}`); }
}

function buildCtx(responses) {
  const ctx = {
    window: {},
    location: { hash: '' },
    URLSearchParams,
    document: { addEventListener() {}, removeEventListener() {} },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    CSApi: { fetch: async (url) => ({ json: async () => responses[url] || {} }) },
    console, setTimeout, clearTimeout,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/region-filter.js', 'utf8'), ctx);
  return ctx.window.RegionFilter;
}

function container() {
  return { innerHTML: '', style: {}, querySelector: () => null };
}

const FREQ = {
  '/api/config/regions': { '909.875': '909.875 MHz', '910.525': 'US 910' },
  '/api/config/region-quick-picks': { quickPicks: [], mode: 'frequency' },
};
const IATA = {
  '/api/config/regions': { SJC: 'SJC', SFO: 'SFO' },
  '/api/config/region-quick-picks': { quickPicks: [] },
};

(async () => {
  console.log('RegionFilter frequency mode');

  await test('observerKey returns the IATA code in iata mode', async () => {
    const rf = buildCtx(IATA);
    await rf.fetchRegions();
    assert.strictEqual(rf.isFrequencyMode(), false);
    assert.strictEqual(rf.observerKey({ iata: 'SMF', radio: '910.525,62.5,7,5' }), 'SMF');
  });

  await test('observerKey returns the rounded frequency in frequency mode', async () => {
    const rf = buildCtx(FREQ);
    await rf.fetchRegions();
    assert.strictEqual(rf.isFrequencyMode(), true);
    assert.strictEqual(rf.observerKey({ iata: 'SMF', radio: '910.5250061,62.5,7,5' }), '910.525');
    assert.strictEqual(rf.observerKey({ iata: 'SMF', radio: '927.875,250,11,5' }), '927.875');
    assert.strictEqual(rf.observerKey({ iata: 'SMF', radio: '910525000,62.5,7,5' }), '910.525');
    assert.strictEqual(rf.observerKey({ iata: 'SMF', radio: '911.0,62.5,7,5' }), '911');
    assert.strictEqual(rf.observerKey({ iata: 'SMF' }), '');
  });

  await test('pills are labelled Frequency with the server labels', async () => {
    const rf = buildCtx(FREQ);
    await rf.fetchRegions();
    const c = container();
    rf.render(c);
    assert.match(c.innerHTML, /Frequency:/);
    assert.match(c.innerHTML, /US 910/);
    assert.match(c.innerHTML, /909\.875 MHz/);
    assert.doesNotMatch(c.innerHTML, /Region:/);
  });

  await test('frequencies are listed in numeric order, whole MHz included', async () => {
    const rf = buildCtx({
      '/api/config/regions': { '927.875': '927.875 MHz', '911': '911 MHz', '909.875': '909.875 MHz' },
      '/api/config/region-quick-picks': { quickPicks: [], mode: 'frequency' },
    });
    await rf.fetchRegions();
    const c = container();
    rf.render(c);
    const order = [...c.innerHTML.matchAll(/data-region="([\d.]+)"/g)].map(m => m[1]);
    assert.deepStrictEqual(order, ['909.875', '911', '927.875']);
  });

  await test('iata mode keeps the Region label', async () => {
    const rf = buildCtx(IATA);
    await rf.fetchRegions();
    const c = container();
    rf.render(c);
    assert.match(c.innerHTML, /Region:/);
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();
