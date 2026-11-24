/* === CoreScope — region-filter.js (shared region filter component) === */
'use strict';

(function () {
  var LS_KEY = 'meshcore-region-filter';
  var _regions = {};       // { code: label }
  var _selected = null;    // Set of selected region codes, null = all
  var _listeners = [];
  var _container = null;
  var _loaded = false;
  var _picks = [];         // configured quick picks: [{ name, description, regions: [code] }]
  var _byFrequency = false; // server regionFilter.mode "frequency": a frequency dropdown beside the region one
  var _freqRegions = {};    // frequency mode: { "910.525": ["OAK", "SJC", ...] }

  /** True for a selection code that is a frequency in MHz ("910.525"). In
   *  frequency mode the selection holds frequencies and IATA codes. */
  function isFrequencyCode(c) { return /^\d+(\.\d+)?$/.test(String(c)); }

  /** Frequency of an observer object in MHz, from radio "MHz,kHz,SF,CR",
   *  rounded to kHz like the server (frequencyRegionKey), or ''. */
  function observerFrequency(o) {
    // Same reading as the server (frequencyRegionKey): unit suffix, else a
    // bare Hz or kHz value scaled by magnitude.
    var s = String(o.radio || '').split(',')[0].trim().toLowerCase();
    var scale = 0;
    if (/mhz$/.test(s)) { s = s.slice(0, -3); scale = 1; }
    else if (/khz$/.test(s)) { s = s.slice(0, -3); scale = 1e-3; }
    else if (/hz$/.test(s)) { s = s.slice(0, -2); scale = 1e-6; }
    var f = Number(s.trim());
    if (!s.trim() || !isFinite(f) || f <= 0) return '';
    if (!scale) scale = f >= 1e7 ? 1e-6 : (f >= 1e4 ? 1e-3 : 1);
    f *= scale;
    if (f < 100 || f > 2500) return '';
    return String(Math.round(f * 1000) / 1000);
  }

  /** Region key of an observer object: its IATA code, or in frequency mode
   *  "<frequency>|<IATA>" like the server's observerRegionKey. */
  function observerKey(o) {
    if (!o) return '';
    if (!_byFrequency) return o.iata || '';
    var iata = String(o.iata || '').trim().toUpperCase();
    var freq = observerFrequency(o);
    return freq || iata ? freq + '|' + iata : '';
  }

  /** True when a region key (observerKey) is in the selection codes. A bare
   *  IATA key is matched case-insensitively against the codes; a frequency
   *  key "<frequency>|<IATA>" must match one of the selected frequencies (if
   *  any) and one of the selected codes (if any), like the server. */
  function keyMatches(key, selected) {
    if (!key || !selected || !selected.length) return false;
    key = String(key);
    var bar = key.indexOf('|');
    if (bar < 0) {
      var k = key.toUpperCase();
      return selected.some(function (c) { return String(c).toUpperCase() === k; });
    }
    var freq = key.slice(0, bar), iata = key.slice(bar + 1).toUpperCase();
    var freqs = [], iatas = [];
    selected.forEach(function (c) {
      if (isFrequencyCode(c)) freqs.push(String(Math.round(parseFloat(c) * 1000) / 1000));
      else iatas.push(String(c).toUpperCase());
    });
    if (freqs.length && freqs.indexOf(freq) < 0) return false;
    if (iatas.length && iatas.indexOf(iata) < 0) return false;
    return true;
  }

  /** Words for a filter control: "Region"/"Regions" or "Frequency"/"Frequencies". */
  function noun(plural, kind) {
    if (kind === 'freq') return plural ? 'Frequencies' : 'Frequency';
    return plural ? 'Regions' : 'Region';
  }

  /** The selected codes of one kind ('freq' or 'region'), as an array. In
   *  iata mode every code is a region. */
  function selectedOfKind(kind) {
    if (!_selected) return [];
    return Array.from(_selected).filter(function (c) {
      return (_byFrequency && isFrequencyCode(c)) === (kind === 'freq');
    });
  }

  /** Region codes offered for the selected frequencies: those with an
   *  observer on one of them, or every region when none is selected. */
  function regionsForFrequencies(freqs, allRegions) {
    if (!freqs.length) return allRegions;
    var on = Object.create(null);
    freqs.forEach(function (f) { (_freqRegions[f] || []).forEach(function (c) { on[c] = true; }); });
    return allRegions.filter(function (c) { return on[c]; });
  }

  /** Replace the selected codes of one kind, keeping the other kind's. A
   *  frequency change drops selected regions with no observer on the new
   *  frequencies, so no hidden choice keeps filtering. */
  function setKindSelection(kind, codes, allRegions) {
    var freqs = kind === 'freq' ? codes : selectedOfKind('freq');
    var regions = kind === 'region' ? codes : selectedOfKind('region');
    if (kind === 'freq' && allRegions) {
      var offered = regionsForFrequencies(freqs, allRegions);
      regions = regions.filter(function (c) { return offered.indexOf(c) >= 0; });
    }
    var all = freqs.concat(regions);
    _selected = all.length ? new Set(all) : null;
  }

  function loadFromStorage() {
    try {
      var stored = JSON.parse(localStorage.getItem(LS_KEY));
      if (Array.isArray(stored) && stored.length > 0) return new Set(stored);
    } catch (e) { /* ignore */ }
    return null; // null = all selected
  }

  function saveToStorage() {
    if (!_selected) {
      localStorage.removeItem(LS_KEY);
    } else {
      localStorage.setItem(LS_KEY, JSON.stringify(Array.from(_selected)));
    }
  }

  _selected = loadFromStorage();
  // A ?region= link applies before any page reads the selection, so even a
  // page that fetches before calling init() uses it on a fresh load.
  applyUrlRegion();

  /** Fetch the configured quick picks. A failure only means no quick picks. */
  async function fetchQuickPicks() {
    try {
      var data = await CSApi.fetch('/api/config/region-quick-picks').then(function (r) { return r.json(); });
      _picks = (data && Array.isArray(data.quickPicks)) ? data.quickPicks : [];
      _byFrequency = !!(data && data.mode === 'frequency');
      _freqRegions = (data && data.frequencyRegions) || {};
    } catch (e) {
      // Offline or an older server without the endpoint: the region filter
      // works exactly as before, just without quick picks.
      _picks = [];
    }
  }

  /** Fetch regions (and quick picks) from server */
  async function fetchRegions() {
    if (_loaded) return _regions;
    var picksLoaded = fetchQuickPicks();
    try {
      var data = await CSApi.fetch('/api/config/regions').then(function (r) { return r.json(); });
      _regions = data || {};
      _loaded = true;
      // If stored selection has codes no longer valid, clean up
      if (_selected) {
        var codes = Object.keys(_regions);
        var cleaned = new Set();
        _selected.forEach(function (c) { if (codes.includes(c)) cleaned.add(c); });
        _selected = cleaned.size > 0 ? cleaned : null;
        saveToStorage();
      }
    } catch (e) {
      _regions = {};
    }
    await picksLoaded;
    return _regions;
  }

  function esc(v) {
    return String(v == null ? '' : v).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  /** Quick picks narrowed to the codes that have an observer today. A pick
   *  with none would select nothing, so it is not offered. */
  function presentPicks(codes) {
    var known = Object.create(null);
    codes.forEach(function (c) { known[c] = true; });
    var out = [];
    _picks.forEach(function (p) {
      var here = (p.regions || []).filter(function (c) { return known[c]; });
      if (here.length) out.push({ name: p.name, description: p.description || '', codes: here });
    });
    return out;
  }

  /** The pick whose codes are exactly the current selection, if any. */
  function activePick(picks) {
    var sel = selectedOfKind('region');
    if (sel.length === 0) return null;
    for (var p of picks) {
      if (p.codes.length !== sel.length) continue;
      if (p.codes.every(function (c) { return sel.indexOf(c) >= 0; })) return p;
    }
    return null;
  }

  function pickButtonsHtml(picks) {
    var active = activePick(picks);
    var html = '';
    picks.forEach(function (p, i) {
      var on = p === active;
      var title = (p.description ? p.description + ' ' : '') + 'Selects ' + p.codes.join(', ') + '.';
      html += '<button type="button" class="region-pill region-quick-pick' + (on ? ' region-pill-active' : '') +
        '" data-pick="' + i + '" aria-pressed="' + on + '" title="' + esc(title) + '">' + esc(p.name) + '</button>';
    });
    return html;
  }

  /** Quick picks as checkbox rows in the region dropdown, styled like All:
   *  ticked while that pick is the selection. */
  function menuPicksHtml(picks) {
    if (!picks.length) return '';
    var active = activePick(picks);
    var html = '';
    picks.forEach(function (p, i) {
      var title = (p.description ? p.description + ' ' : '') + 'Selects ' + p.codes.join(', ') + '.';
      html += '<label class="region-dropdown-item region-quick-pick" title="' + esc(title) + '">' +
        '<input type="checkbox" data-pick="' + i + '"' + (p === active ? ' checked' : '') + '> <strong>' + esc(p.name) + '</strong></label>';
    });
    return html + '<div class="region-dropdown-sep" role="separator"></div>';
  }

  /** Quick picks inside the pill bar, ahead of the single regions. */
  function barPicksHtml(picks) {
    if (!picks.length) return '';
    return pickButtonsHtml(picks) + '<span class="region-quick-picks-divider" aria-hidden="true"></span>';
  }

  /** Tapping a pick selects its codes; tapping the active pick goes back to all. */
  // Like a click in the control, a pick saves, redraws and tells the page,
  // so the page re-queries straight away.
  function applyPick(picks, index) {
    var p = picks[index];
    if (!p) return;
    setKindSelection('region', activePick(picks) === p ? [] : p.codes.slice());
    saveToStorage();
    if (_container) render(_container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Get selected regions as array, or null if all */
  function getSelected() {
    if (!_selected || _selected.size === 0) return null;
    return Array.from(_selected);
  }

  /** Get region query param string for API calls: "SJC,SFO" or empty */
  function getRegionParam() {
    var sel = getSelected();
    return sel ? sel.join(',') : '';
  }

  /** Build query string fragment: "&region=SJC,SFO" or "" */
  function regionQueryString() {
    var p = getRegionParam();
    return p ? '&region=' + encodeURIComponent(p) : '';
  }

  /** Handle a region toggle (shared logic for both pill and dropdown modes) */
  function toggleRegion(region, codes, container) {
    if (region === '__all__') {
      _selected = null;
    } else {
      if (!_selected) {
        _selected = new Set([region]);
      } else if (_selected.has(region)) {
        _selected.delete(region);
        if (_selected.size === 0) _selected = null;
      } else {
        _selected.add(region);
      }
      if (_selected && _selected.size === codes.length) _selected = null;
    }
    saveToStorage();
    render(container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Summary label for a dropdown trigger. ctl is the control (see
   *  regionControl / frequencyControl). */
  function dropdownLabel(ctl) {
    var sel = ctl.selected();
    if (!sel.length) return 'All ' + noun(true, ctl.kind);
    var named = ctl.picks.length ? activePick(ctl.picks) : null;
    if (named) return esc(named.name);
    if (sel.length <= 2) return sel.map(function (c) { return ctl.kind === 'freq' ? c + ' MHz' : c; }).join(', ');
    return sel.length + ' ' + noun(true, ctl.kind);
  }

  /** The region control: IATA codes (in frequency mode, those on the
   *  selected frequencies), with the quick picks. */
  function regionControl(codes) {
    return {
      kind: 'region',
      codes: codes,
      picks: presentPicks(codes),
      selected: function () { return selectedOfKind('region'); },
      label: function (code) {
        var configLabel = _regions[code];
        // Frequency mode lists an unconfigured code with itself as its label;
        // the city name reads better there.
        if (_byFrequency && configLabel === code) configLabel = '';
        var cityName = configLabel || (window.IATA_CITIES && window.IATA_CITIES[code]);
        return cityName ? (code + ' - ' + cityName) : code;
      }
    };
  }

  /** The frequency control (frequency mode only). allRegions is every
   *  region code, to narrow the region selection when this one changes. */
  function frequencyControl(codes, allRegions) {
    return {
      kind: 'freq',
      codes: codes,
      picks: [],
      allRegions: allRegions,
      selected: function () { return selectedOfKind('freq'); },
      // The server's label is "910.525 MHz" or a configured name.
      label: function (code) { return _regions[code] || (code + ' MHz'); }
    };
  }

  /** Toggle one code of a control, or all of them ('__all__'), then save,
   *  redraw and tell the page. Ticking a code while all are shown selects
   *  only it; ticking the last unticked one goes back to all. */
  function toggleInControl(ctl, code) {
    var sel = ctl.selected();
    if (code === '__all__') {
      sel = [];
    } else if (!sel.length) {
      sel = [code];
    } else if (sel.indexOf(code) >= 0) {
      sel = sel.filter(function (c) { return c !== code; });
    } else {
      sel = sel.concat([code]);
      if (sel.length === ctl.codes.length) sel = [];
    }
    setKindSelection(ctl.kind, sel, ctl.allRegions);
    saveToStorage();
    if (_container) render(_container);
    _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Render pill bar mode (≤4 regions) */
  function renderPills(container, codes) {
    var allSelected = !_selected;
    var picks = presentPicks(codes);
    var html = '<div class="region-filter-bar" role="group" aria-label="' + noun(false, 'region') + ' filter">';
    html += '<span class="region-filter-label" id="region-filter-label">' + noun(false, 'region') + ':</span>';
    html += barPicksHtml(picks);
    html += '<button class="region-pill' + (allSelected ? ' region-pill-active' : '') +
      '" data-region="__all__" role="checkbox" aria-checked="' + allSelected + '">All</button>';
    codes.forEach(function (code) {
      var label = _regions[code] || code;
      var active = allSelected || (_selected && _selected.has(code));
      html += '<button class="region-pill' + (active ? ' region-pill-active' : '') +
        '" data-region="' + code + '" role="checkbox" aria-checked="' + !!active + '">' + label + '</button>';
    });
    html += '</div>';
    container.innerHTML = html;

    container.onclick = function (e) {
      var pickBtn = e.target.closest('[data-pick]');
      if (pickBtn) { applyPick(picks, Number(pickBtn.dataset.pick)); return; }
      var btn = e.target.closest('[data-region]');
      if (!btn) return;
      toggleRegion(btn.dataset.region, codes, container);
    };
  }

  /** Render one dropdown control into container. Returns a cleanup
   *  function that removes its outside-click listener. */
  function renderDropdown(container, ctl) {
    var sel = ctl.selected();
    var allSelected = !sel.length;
    var html = '<div class="region-dropdown-wrap" role="group" aria-label="' + noun(false, ctl.kind) + ' filter">';
    html += '<button class="region-dropdown-trigger" aria-haspopup="listbox" aria-expanded="false">' +
      dropdownLabel(ctl) + ' ▾</button>';
    html += '<div class="region-dropdown-menu" role="listbox" aria-label="Select ' + noun(true, ctl.kind).toLowerCase() + '" hidden>';
    html += '<label class="region-dropdown-item"><input type="checkbox" data-region="__all__"' +
      (allSelected ? ' checked' : '') + '> <strong>All</strong></label>';
    html += menuPicksHtml(ctl.picks);
    ctl.codes.forEach(function (code) {
      var active = allSelected || sel.indexOf(code) >= 0;
      html += '<label class="region-dropdown-item"><input type="checkbox" data-region="' + esc(code) + '"' +
        (active ? ' checked' : '') + '> ' + esc(ctl.label(code)) + '</label>';
    });
    html += '</div></div>';
    container.innerHTML = html;

    var trigger = container.querySelector('.region-dropdown-trigger');
    var menu = container.querySelector('.region-dropdown-menu');
    container.onclick = null;

    trigger.onclick = function () {
      var open = !menu.hidden;
      menu.hidden = open;
      trigger.setAttribute('aria-expanded', String(!open));
    };

    menu.onchange = function (e) {
      var input = e.target;
      if (input.dataset.pick) { applyPick(ctl.picks, Number(input.dataset.pick)); return; }
      if (!input.dataset.region) return;
      toggleInControl(ctl, input.dataset.region);
    };

    // Close on outside click
    function onDocClick(e) {
      if (!container.contains(e.target)) {
        menu.hidden = true;
        trigger.setAttribute('aria-expanded', 'false');
      }
    }
    document.addEventListener('click', onDocClick, true);
    return function () { document.removeEventListener('click', onDocClick, true); };
  }

  /** Frequency mode: a frequency dropdown and a region dropdown side by
   *  side. The region one lists the codes on the selected frequencies. */
  function renderPair(container, codes) {
    var freqs = codes.filter(isFrequencyCode)
      .sort(function (a, b) { return parseFloat(a) - parseFloat(b); });
    var allRegions = codes.filter(function (c) { return !isFrequencyCode(c); }).sort();
    var regions = regionsForFrequencies(selectedOfKind('freq'), allRegions);
    container.innerHTML = '<div class="region-filter-pair">' +
      '<div class="region-filter-part" data-kind="freq"></div>' +
      '<div class="region-filter-part" data-kind="region"></div></div>';
    container.onclick = null;
    var cleanups = [];
    var freqEl = container.querySelector('[data-kind="freq"]');
    var regionEl = container.querySelector('[data-kind="region"]');
    if (freqs.length >= 2) cleanups.push(renderDropdown(freqEl, frequencyControl(freqs, allRegions)));
    else freqEl.style.display = 'none';
    if (regions.length >= 2) cleanups.push(renderDropdown(regionEl, regionControl(regions)));
    else regionEl.style.display = 'none';
    container._regionCleanup = function () { cleanups.forEach(function (fn) { fn(); }); };
    return freqs.length >= 2 || regions.length >= 2;
  }

  /** Render the filter bar into a container element */
  // The outside-click listener closes over the container; leaving it on
  // document pins a departed page's detached DOM.
  function release(container) {
    if (container && container._regionCleanup) { container._regionCleanup(); container._regionCleanup = null; }
  }

  function render(container) {
    if (_container && _container !== container) release(_container);
    _container = container;
    release(container);

    var codes = Object.keys(_regions);
    if (_byFrequency) {
      container.style.display = renderPair(container, codes) ? '' : 'none';
      return;
    }
    if (codes.length < 2) {
      container.innerHTML = '';
      container.style.display = 'none';
      return;
    }
    container.style.display = '';

    if (codes.length > 4 || container._forceDropdown) {
      container._regionCleanup = renderDropdown(container, regionControl(codes));
    } else {
      renderPills(container, codes);
    }
  }

  /** Subscribe to selection changes. Callback receives selected array or null */
  function onChange(fn) {
    _listeners.push(fn);
    return fn;
  }

  /** Unsubscribe */
  function offChange(fn) {
    _listeners = _listeners.filter(function (f) { return f !== fn; });
  }

  /** Region codes from a ?region= hash param ("#/live?region=SJC,SFO"), or
   *  null when the URL doesn't set one. */
  function regionsFromUrl() {
    if (typeof location === 'undefined' || !location.hash) return null;
    var raw = new URLSearchParams(location.hash.split('?')[1] || '').get('region');
    if (!raw) return null;
    var codes = raw.split(',').map(function (c) { return c.trim(); }).filter(Boolean);
    return codes.length > 0 ? codes : null;
  }

  /** Apply a ?region= URL param as the selection, saved like a click in the
   *  control. Returns true when that changed the selection. */
  function applyUrlRegion() {
    var codes = regionsFromUrl();
    if (!codes) return false;
    // Once regions have loaded, fetchRegions() no longer cleans the selection,
    // so drop unknown codes here; with none left, that's all regions, the same
    // as the cleanup does on a fresh load.
    if (_loaded) codes = codes.filter(function (c) { return Object.prototype.hasOwnProperty.call(_regions, c); });
    var next = codes.length > 0 ? new Set(codes) : null;
    var changed = !sameSelection(_selected, next);
    _selected = next;
    saveToStorage();
    return changed;
  }

  /** Initialize filter in a container, fetch regions, render, return promise.
   *  Options: { dropdown: true } to force dropdown mode regardless of region count
   *
   *  A ?region= param in the page's URL overrides the saved selection, so any
   *  page with a region filter can be deep-linked to a region. On a fresh load
   *  it was already applied when this script ran. On in-app navigation it's
   *  applied here, before the await, so a page that loads after init() uses
   *  it; if it changed the selection, listeners are told once regions load,
   *  which reloads a page that fetched before calling init(). Unknown codes
   *  are dropped either way. */
  async function initFilter(container, opts) {
    if (_container && _container !== container) release(_container);
    _container = container;
    if (opts && opts.dropdown) container._forceDropdown = true;
    var urlChanged = applyUrlRegion();
    await fetchRegions();
    // The page may have been torn down (destroy) while regions were loading.
    if (_container !== container) return;
    render(container);
    if (urlChanged) _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /** Release the document listener and references held for `container`
   *  (default: the current one). Call from a page's destroy(). */
  function destroy(container) {
    var c = container || _container;
    release(c);
    if (!container || container === _container) _container = null;
  }

  /** True when two selections (Set or null = all) hold the same codes. */
  function sameSelection(a, b) {
    var aEmpty = !a || a.size === 0, bEmpty = !b || b.size === 0;
    if (aEmpty || bEmpty) return aEmpty && bEmpty;
    if (a.size !== b.size) return false;
    var same = true;
    a.forEach(function (c) { if (!b.has(c)) same = false; });
    return same;
  }

  /** Override selected regions (e.g. from URL param or a quick pick). Persists
   *  to localStorage, re-renders, and notifies onChange listeners when the
   *  selection actually changed, exactly as a click in the control does. */
  function setSelected(codesArray) {
    var next = (codesArray && codesArray.length > 0) ? new Set(codesArray) : null;
    var changed = !sameSelection(_selected, next);
    _selected = next;
    saveToStorage();
    if (_container) render(_container);
    if (changed) _listeners.forEach(function (fn) { fn(getSelected()); });
  }

  /**
   * #1108 — "Show all nodes (faded)" toggle.
   *
   * When a region is selected, the default behavior (showAll = false) is to
   * HIDE non-region nodes on the map: the operator is looking at a region for
   * a reason, and far-away nodes are visual noise. When the toggle is ON
   * (showAll = true), legacy behavior is restored — all nodes load, region
   * scoping only applies to packet feeds / metrics.
   *
   * State persists across reloads in localStorage. Default: false (hide).
   */
  var SHOW_ALL_KEY = 'mc-region-show-all-nodes';
  var _showAllListeners = [];
  function showAllGet() {
    try { return localStorage.getItem(SHOW_ALL_KEY) === 'true'; }
    catch (e) { return false; }
  }
  function showAllSet(v) {
    var bool = !!v;
    try {
      if (bool) localStorage.setItem(SHOW_ALL_KEY, 'true');
      else localStorage.removeItem(SHOW_ALL_KEY);
    } catch (e) { /* ignore */ }
    _showAllListeners.forEach(function (fn) { fn(bool); });
  }
  function showAllOnChange(fn) { _showAllListeners.push(fn); return fn; }
  function showAllOffChange(fn) {
    _showAllListeners = _showAllListeners.filter(function (f) { return f !== fn; });
  }

  /**
   * Build a node-list query fragment that respects the "show all nodes" toggle.
   * Returns "&region=SJC,SFO" only when a region is selected AND showAll is
   * OFF; otherwise empty string. Use this for /api/nodes? requests on map
   * surfaces where the operator expects the visible markers to follow the
   * region selector. Other surfaces (packets, metrics) should keep using
   * regionQueryString() which is unconditional.
   */
  function nodesRegionQueryString() {
    if (showAllGet()) return '';
    return regionQueryString();
  }

  // Expose globally
  window.RegionFilter = {
    init: initFilter,
    render: render,
    destroy: destroy,
    getSelected: getSelected,
    getRegionParam: getRegionParam,
    observerKey: observerKey,
    keyMatches: keyMatches,
    isFrequencyMode: function () { return _byFrequency; },
    regionQueryString: regionQueryString,
    nodesRegionQueryString: nodesRegionQueryString,
    onChange: onChange,
    offChange: offChange,
    fetchRegions: fetchRegions,
    setSelected: setSelected
  };
  window.RegionShowAll = {
    get: showAllGet,
    set: showAllSet,
    onChange: showAllOnChange,
    offChange: showAllOffChange,
    STORAGE_KEY: SHOW_ALL_KEY
  };
})();
