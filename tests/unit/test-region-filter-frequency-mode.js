/**
 * Region filter frequency mode (config regionFilter.mode "frequency"). The
 * server lists frequencies and IATA codes in /api/config/regions and says so
 * with mode: "frequency" (plus the codes on each frequency) in
 * /api/config/region-quick-picks. The filter then shows a Frequency dropdown
 * beside the Region one; the Region one lists the codes on the selected
 * frequencies. RegionFilter.observerKey(o) returns "<frequency>|<IATA>" and
 * RegionFilter.keyMatches matches it against a selection holding both kinds,
 * like the server.
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
  const store = {};
  const ctx = {
    window: {},
    location: { hash: '' },
    URLSearchParams,
    document: { addEventListener() {}, removeEventListener() {} },
    localStorage: {
      getItem: (k) => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = v; },
      removeItem: (k) => { delete store[k]; },
    },
    CSApi: { fetch: async (url) => ({ json: async () => responses[url] || {} }) },
    console, setTimeout, clearTimeout,
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/region-filter.js', 'utf8'), ctx);
  return ctx.window.RegionFilter;
}

/** A fake element: querySelector hands out one child fake per selector, so
 *  a render can fill sub-containers and wire handlers the test can call. */
function el() {
  const kids = {};
  return {
    innerHTML: '', style: {}, dataset: {},
    contains: () => true,
    setAttribute() {},
    querySelector(sel) { return kids[sel] || (kids[sel] = el()); },
  };
}
const FREQ_PART = '[data-kind="freq"]';
const REGION_PART = '[data-kind="region"]';

/** Tick a checkbox in a rendered dropdown part. */
function tick(part, code) {
  part.querySelector('.region-dropdown-menu').onchange({ target: { dataset: { region: code } } });
}

const FREQ = {
  '/api/config/regions': {
    '909.875': '909.875 MHz', '910.525': 'US 910',
    SMF: 'SMF', SJC: 'SJC', OAK: 'Oakland', SFO: 'SFO',
  },
  '/api/config/region-quick-picks': {
    quickPicks: [{ name: 'Bay', regions: ['SJC', 'OAK', 'SFO'] }],
    mode: 'frequency',
    frequencyRegions: { '909.875': ['SMF'], '910.525': ['OAK', 'SFO', 'SJC'] },
  },
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

  await test('observerKey returns "<frequency>|<IATA>" in frequency mode', async () => {
    const rf = buildCtx(FREQ);
    await rf.fetchRegions();
    assert.strictEqual(rf.isFrequencyMode(), true);
    assert.strictEqual(rf.observerKey({ iata: 'smf', radio: '910.5250061,62.5,7,5' }), '910.525|SMF');
    assert.strictEqual(rf.observerKey({ iata: 'SAN', radio: '927.875,250,11,5' }), '927.875|SAN');
    assert.strictEqual(rf.observerKey({ iata: 'SJC', radio: '910525000,62.5,7,5' }), '910.525|SJC');
    assert.strictEqual(rf.observerKey({ iata: 'RNO', radio: '911.0,62.5,7,5' }), '911|RNO');
    assert.strictEqual(rf.observerKey({ iata: 'SMF' }), '|SMF');
    assert.strictEqual(rf.observerKey({}), '');
  });

  await test('keyMatches combines frequencies and codes like the server', async () => {
    const rf = buildCtx(FREQ);
    const m = (key, sel) => rf.keyMatches(key, sel);
    assert.strictEqual(m('SJC', ['sjc']), true);
    assert.strictEqual(m('SJC', ['SFO']), false);
    assert.strictEqual(m('910.525|SJC', ['910.525']), true);
    assert.strictEqual(m('910.525|SJC', ['SJC']), true);
    assert.strictEqual(m('910.525|SJC', ['910.525', 'SJC']), true);
    assert.strictEqual(m('910.525|SJC', ['910.525', 'SFO']), false);
    assert.strictEqual(m('910.525|SJC', ['909.875', 'SJC']), false);
    assert.strictEqual(m('910.525|SJC', ['910.5250', 'OAK', 'SJC']), true);
    assert.strictEqual(m('|SJC', ['910.525', 'SJC']), false);
    assert.strictEqual(m('', ['SJC']), false);
  });

  await test('frequency mode shows a Frequency and a Region dropdown', async () => {
    const rf = buildCtx(FREQ);
    await rf.fetchRegions();
    const c = el();
    rf.render(c);
    const freq = c.querySelector(FREQ_PART), region = c.querySelector(REGION_PART);
    assert.match(freq.innerHTML, /All Frequencies/);
    assert.match(freq.innerHTML, /US 910/);
    assert.match(freq.innerHTML, /909\.875 MHz/);
    assert.doesNotMatch(freq.innerHTML, /data-region="SJC"/);
    assert.match(region.innerHTML, /All Regions/);
    assert.match(region.innerHTML, /OAK - Oakland/);
    assert.match(region.innerHTML, /data-region="SMF"/);
    assert.match(region.innerHTML, /Bay/);
    assert.doesNotMatch(region.innerHTML, /data-region="910\.525"/);
  });

  await test('frequencies are listed in numeric order, whole MHz included', async () => {
    const rf = buildCtx({
      '/api/config/regions': { '927.875': '927.875 MHz', '911': '911 MHz', '909.875': '909.875 MHz' },
      '/api/config/region-quick-picks': { quickPicks: [], mode: 'frequency' },
    });
    await rf.fetchRegions();
    const c = el();
    rf.render(c);
    const order = [...c.querySelector(FREQ_PART).innerHTML.matchAll(/data-region="([\d.]+)"/g)].map(m => m[1]);
    assert.deepStrictEqual(order, ['909.875', '911', '927.875']);
  });

  await test('a frequency narrows the regions, and a region adds to it', async () => {
    const rf = buildCtx(FREQ);
    const c = el();
    await rf.init(c);
    tick(c.querySelector(FREQ_PART), '910.525');
    assert.strictEqual(rf.getRegionParam(), '910.525');
    const region = c.querySelector(REGION_PART);
    assert.doesNotMatch(region.innerHTML, /data-region="SMF"/);
    assert.match(region.innerHTML, /data-region="SJC"/);
    tick(region, 'SJC');
    assert.strictEqual(rf.getRegionParam(), '910.525,SJC');
    assert.match(c.querySelector(REGION_PART).innerHTML, />SJC ▾/);
  });

  await test('switching frequency drops regions not on it', async () => {
    const rf = buildCtx(FREQ);
    const c = el();
    await rf.init(c);
    tick(c.querySelector(REGION_PART), 'SJC');
    tick(c.querySelector(REGION_PART), 'SMF');
    assert.deepStrictEqual([...rf.getSelected()].sort(), ['SJC', 'SMF']);
    tick(c.querySelector(FREQ_PART), '909.875');
    assert.strictEqual(rf.getRegionParam(), '909.875,SMF');
    tick(c.querySelector(FREQ_PART), '__all__');
    assert.strictEqual(rf.getRegionParam(), 'SMF');
  });

  await test('a quick pick sets the regions and keeps the frequency', async () => {
    const rf = buildCtx(FREQ);
    const c = el();
    await rf.init(c);
    tick(c.querySelector(FREQ_PART), '910.525');
    c.querySelector(REGION_PART).querySelector('.region-dropdown-menu')
      .onchange({ target: { dataset: { pick: '0' } } });
    assert.deepStrictEqual([...rf.getSelected()].sort(), ['910.525', 'OAK', 'SFO', 'SJC']);
    assert.match(c.querySelector(REGION_PART).innerHTML, />Bay ▾/);
  });

  await test('iata mode keeps the Region pills', async () => {
    const rf = buildCtx(IATA);
    await rf.fetchRegions();
    const c = el();
    rf.render(c);
    assert.match(c.innerHTML, /Region:/);
    assert.doesNotMatch(c.innerHTML, /Frequenc/);
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();
