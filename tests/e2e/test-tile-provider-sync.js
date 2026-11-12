/* Local fixture only. Real Leaflet maps, offline tiles, provider/theme/config
 * transitions. Run: BASE_URL=http://localhost:13581 node <this file>. */
'use strict';
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture, not production');
const tiles = { lightDefault: 'usgs-topo', darkDefault: 'carto-dark', providers: { carto: { enabled: true }, usgs: { enabled: true }, opentopomap: { enabled: true } } };
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  try {
    for (const route of ['map', 'live', 'nodes']) {
      const context = await browser.newContext({ colorScheme: 'light' });
      await context.addInitScript(() => {
        localStorage.setItem('theme', 'light');
        window.__tileTestMaps = [];
        document.addEventListener('DOMContentLoaded', () => {
          L.Map.addInitHook(function () { window.__tileTestMaps.push(this); });
        });
      });
      const page = await context.newPage();
      // No vendor tile request leaves the test. Rendering and zoom behavior
      // are exercised against a deterministic transparent image.
      await page.route('**/*', async request => {
        const u = new URL(request.request().url());
        if (u.origin !== new URL(BASE).origin) return request.fulfill({ contentType: 'image/png', body: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j1ioAAAAASUVORK5CYII=', 'base64') });
        if (u.pathname === '/api/config/client') {
          const response = await request.fetch();
          const config = await response.json();
          config.map = { ...config.map, tiles };
          delete config.tiles;
          return request.fulfill({ response, json: config });
        }
        return request.continue();
      });
      await page.goto(BASE + '/#/' + route);
      await page.evaluate(async () => { await window.MeshConfigReady; document.documentElement.setAttribute('data-theme', 'light'); });
      if (route === 'nodes') {
        await page.waitForFunction(() => typeof window._applyTilesToNodeMap === 'function');
        // The shared production helper powers node/reach/coverage maps.
        await page.evaluate(() => {
          const div = document.createElement('div'); div.id = 'tile-test-node'; div.style.cssText = 'width:600px;height:400px'; document.body.appendChild(div);
          window.__tileTestNode = L.map(div).setView([38.6, -121.1], 12);
          window._applyTilesToNodeMap(window.__tileTestNode);
        });
      }
      const id = route === 'map' ? 'leaflet-map' : route === 'live' ? 'liveMap' : 'tile-test-node';
      async function check(maxZoom, urlPart) {
        await page.waitForFunction(({ id, maxZoom, urlPart }) => {
          const m = window.__tileTestNode || window.__tileTestMaps.find(m => m.getContainer().id === id);
          if (!m) return false;
          const layers = []; m.eachLayer(l => { if (l instanceof L.TileLayer) layers.push(l); });
          return layers.length === 1 && layers.every(l => l.options.maxZoom === maxZoom && l._url.includes(urlPart)) && m.getMaxZoom() === maxZoom && !m._animatingZoom;
        }, { id, maxZoom, urlPart });
        const zoom = await page.evaluate(id => {
          const m = window.__tileTestNode || window.__tileTestMaps.find(m => m.getContainer().id === id);
          m.stop(); m.setZoom(22, { animate: false }); return m.getZoom();
        }, id);
        assert.strictEqual(zoom, maxZoom, route + ' clamps actual map zoom');
      }
      await check(16, 'USGSTopo');
      await page.evaluate(() => MC_setLightTileProvider('carto-light'));
      await check(19, 'light_all');
      await page.evaluate(() => MC_setLightTileProvider('opentopomap'));
      await check(17, 'opentopomap');
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
      await check(19, 'dark_all');
      await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'light'));
      await check(17, 'opentopomap');
      // Late config rebuild must update an existing map, without remounting.
      await page.evaluate(() => {
        localStorage.removeItem('mc-light-tile-provider');
        window.MC_MAP_CFG.tiles.lightDefault = 'usgs-topo';
        MC_initTileRegistry(true);
      });
      await check(16, 'USGSTopo');
      for (let repeat = 0; repeat < 3; repeat++) {
        await page.evaluate(() => MC_setLightTileProvider('carto-light'));
        await check(19, 'light_all');
        await page.evaluate(() => MC_setLightTileProvider('usgs-topo'));
        await check(16, 'USGSTopo');
      }
      console.log('PASS ' + route + ': fresh, provider switch, theme switch, late config, zoom clamp');
      await context.close();
    }
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
