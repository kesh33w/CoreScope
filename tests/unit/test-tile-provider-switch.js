'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const root = path.resolve(__dirname, '../..');
const ctx = {
  window: { addEventListener() {}, matchMedia: () => ({ matches: false }) },
  document: { documentElement: { getAttribute: () => 'light' }, querySelectorAll: () => [] },
  localStorage: { getItem: () => null, setItem() {} },
  console, CustomEvent: function () {},
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(root, 'public/map-tile-providers.js'), 'utf8'), ctx);
assert.strictEqual(typeof ctx.window.MC_updateTileLayer, 'function');
const calls = [];
const layer = { options: { maxZoom: 19, attribution: 'old' }, setUrl(url) { this.url = url; calls.push('url'); }, addTo(group) { assert.strictEqual(group, parent); calls.push('add'); parent.added = this; } };
const parent = { removeLayer(l) { assert.strictEqual(l, layer); calls.push('remove'); } };
ctx.window.MC_updateTileLayer(layer, parent, { url: 'usgs/{z}', maxZoom: 16, attribution: 'USGS' });
assert.strictEqual(layer.options.maxZoom, 16);
assert.strictEqual(layer.options.attribution, 'USGS');
assert.strictEqual(layer.url, 'usgs/{z}');
assert.deepStrictEqual(calls, ['remove', 'url', 'add']);
ctx.window.MC_updateTileLayer(layer, parent, { url: 'carto/{z}', maxZoom: 20, attribution: 'Carto' });
assert.strictEqual(layer.options.maxZoom, 20);

// Exercise the actual page handlers, not copies of their implementation.
for (const [file, fn, resolver, refName, groupName] of [
  ['map.js', '_syncDarkTiles', '_resolveTileUrl', '_darkRefLayer', 'autoLayerGroup'],
  ['live.js', '_liveSyncDarkTiles', '_liveResolveTile', '_liveDarkRefLayer', 'liveAutoLayerGroup'],
]) {
  const source = fs.readFileSync(path.join(root, 'public', file), 'utf8');
  const start = source.indexOf('function ' + fn + '(dark)');
  const end = source.indexOf('\n    const _', start);
  assert.ok(start >= 0 && end > start);
  const updates = [];
  const base = { options: { maxZoom: 19 }, setUrl() {} };
  const group = { removeLayer() {}, hasLayer: () => true };
  let r = { url: 'base', maxZoom: 16, attribution: 'a', refUrl: 'labels' };
  const c = { window: { MC_updateTileLayer: (l, g, d) => { updates.push(d); l.options.maxZoom = d.maxZoom; }, MC_applyTileFilter() {} }, tileLayer: base, map: {}, L: { tileLayer: (url, options) => ({ options, addTo: () => ({ options, setUrl() {} }) }) } };
  c[resolver] = () => r; c[refName] = null; c[groupName] = group;
  vm.createContext(c); vm.runInContext(source.slice(start, end), c);
  c[fn](true);
  assert.strictEqual(base.options.maxZoom, 16, file + ': switch to lower zoom');
  assert.strictEqual(c[refName].options.maxZoom, 16, file + ': labels use provider zoom');
  r = { ...r, maxZoom: 20 };
  c[fn](true);
  assert.strictEqual(base.options.maxZoom, 20, file + ': switch back to higher zoom');
  assert.strictEqual(c[refName].options.maxZoom, 20, file + ': existing labels update');
}
console.log('tile provider switch regressions passed');
