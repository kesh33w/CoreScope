/* === CoreScope — compare.js === */
/* Observer packet comparison — Fixes #129 */
'use strict';

/**
 * Compare two sets of packet hashes using Set operations.
 * Returns { onlyA, onlyB, both } as arrays of hashes.
 * O(n) via Set lookups — no nested loops.
 */
function comparePacketSets(hashesA, hashesB) {
  var setA = hashesA instanceof Set ? hashesA : new Set(hashesA || []);
  var setB = hashesB instanceof Set ? hashesB : new Set(hashesB || []);
  var onlyA = [];
  var onlyB = [];
  var both = [];
  setA.forEach(function (h) {
    if (setB.has(h)) both.push(h);
    else onlyA.push(h);
  });
  setB.forEach(function (h) {
    if (!setA.has(h)) onlyB.push(h);
  });
  return { onlyA: onlyA, onlyB: onlyB, both: both };
}

/**
 * Filter packets by route type.
 * mode: 'all' | 'flood' | 'direct'
 * Flood = route_type 0 (TransportFlood) or 1 (Flood)
 * Direct = route_type 2 (Direct) or 3 (TransportDirect)
 */
function filterPacketsByRoute(packets, mode) {
  if (!packets || mode === 'all') return packets || [];
  if (mode === 'flood') {
    return packets.filter(function (p) { return p.route_type === 0 || p.route_type === 1; });
  }
  if (mode === 'direct') {
    return packets.filter(function (p) { return p.route_type === 2 || p.route_type === 3; });
  }
  return packets;
}

/**
 * Compute asymmetric overlap statistics between two observer packet sets.
 * Given a comparePacketSets() result, returns:
 *   - totalA / totalB: unique packet count for each observer
 *   - shared: packets seen by both
 *   - onlyA / onlyB: exclusive packet counts
 *   - aSeesOfB: percentage of B's packets that A also saw (rounded to 0.1%)
 *   - bSeesOfA: percentage of A's packets that B also saw (rounded to 0.1%)
 * Returns 0% (not NaN) when a denominator is zero.
 */
function computeOverlapStats(cmp) {
  var onlyA = (cmp && cmp.onlyA && cmp.onlyA.length) || 0;
  var onlyB = (cmp && cmp.onlyB && cmp.onlyB.length) || 0;
  var shared = (cmp && cmp.both && cmp.both.length) || 0;
  var totalA = onlyA + shared;
  var totalB = onlyB + shared;
  var aSeesOfB = totalB > 0 ? Math.round((shared / totalB) * 1000) / 10 : 0;
  var bSeesOfA = totalA > 0 ? Math.round((shared / totalA) * 1000) / 10 : 0;
  return {
    totalA: totalA,
    totalB: totalB,
    shared: shared,
    onlyA: onlyA,
    onlyB: onlyB,
    aSeesOfB: aSeesOfB,
    bSeesOfA: bSeesOfA,
  };
}

// Reception statistics preserve relay-path copies rather than counting only hashes.
function computeReceptionStats(a, b) {
  function summarize(rows) {
    var hashes = new Set(), paths = new Map(), frames = new Set(), missing = 0;
    rows.forEach(function(p) {
      if (p.hash) hashes.add(p.hash);
      if (p.path_json && p.hash) {
        var path;
        try { path = JSON.stringify(JSON.parse(p.path_json)); } catch (_) { path = null; }
        if (path) { if (!paths.has(p.hash)) paths.set(p.hash, new Set()); paths.get(p.hash).add(path); }
      }
      if (p.raw_hex) frames.add(String(p.raw_hex).toLowerCase()); else missing++;
    });
    var alternate = 0; paths.forEach(function(v) { alternate += Math.max(0, v.size - 1); });
    return { receptions: rows.length, messages: hashes.size, frames: frames.size, alternatePaths: alternate, missingRaw: missing };
  }
  function byFrame(rows) {
    var frames = new Map();
    rows.forEach(function(p) {
      if (!p.raw_hex || p.snr == null || p.snr === '' || !Number.isFinite(Number(p.snr)) || !Number.isFinite(Number(p.timestamp))) return;
      var key = String(p.raw_hex).toLowerCase();
      if (!frames.has(key)) frames.set(key, []);
      frames.get(key).push(p);
    });
    frames.forEach(function(v) { v.sort(function(x,y) { return Number(x.timestamp)-Number(y.timestamp); }); });
    return frames;
  }
  var fa = byFrame(a), fb = byFrame(b), deltas = [];
  fa.forEach(function(aa, key) {
    var bb = fb.get(key); if (!bb) return;
    // Sorted one-to-one matching. Never pair a frame twice, never match by hash alone.
    var i=0,j=0;
    while (i<aa.length && j<bb.length) {
      var dt=Number(aa[i].timestamp)-Number(bb[j].timestamp);
      if (dt < -10) i++; else if (dt > 10) j++; else {
        deltas.push(Number(aa[i].snr)-Number(bb[j].snr)); i++;j++;
      }
    }
  });
  deltas.sort(function(x,y){return x-y;});
  var n=deltas.length;
  return {a:summarize(a),b:summarize(b),paired:{count:n,median:n ? (deltas[Math.floor((n-1)/2)]+deltas[Math.floor(n/2)])/2 : null}};
}

// Notes never leave this browser, enter the URL, or touch the measurement DB.
function cleanExperimentNotes(notes) {
  if (!Array.isArray(notes)) return [];
  return notes.filter(function(n) { return n && typeof n.name==='string' && Number.isFinite(Date.parse(n.at)) && typeof n.a==='string' && typeof n.b==='string'; })
    .slice(-50).map(function(n) { return {name:n.name.slice(0,160),at:new Date(n.at).toISOString(),a:n.a.slice(0,256),b:n.b.slice(0,256)}; });
}

// Expose for testing
if (typeof window !== 'undefined') {
  window.comparePacketSets = comparePacketSets;
  window.filterPacketsByRoute = filterPacketsByRoute;
  window.computeOverlapStats = computeOverlapStats;
}

(function () {
  var PAYLOAD_LABELS = { 0: 'Request', 1: 'Response', 2: 'Direct Msg', 3: 'ACK', 4: 'Advert', 5: 'Channel Msg', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 11: 'Control' };
  var windowMinutes = 60;
  var endTime = '';
  var experimentMode = false;
  var receptionStats = null;
  var baselineStats = null;
  var sampleInfo = null;
  var baselineInfo = null;
  var requestGeneration = 0;
  var NOTES_KEY = 'corescope-experiment-notes-v1';
  var observers = [];
  var selA = null;
  var selB = null;
  var comparisonResult = null;
  var packetsA = [];
  var packetsB = [];
  var currentView = 'summary';
  var routeFilter = 'all';

  function init(app, routeParam) {
    // Parse preselected observers from URL: #/compare?a=ID1&b=ID2
    var hashParams = location.hash.split('?')[1] || '';
    var params = new URLSearchParams(hashParams);
    selA = params.get('a') || null;
    selB = params.get('b') || null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
    currentView = 'summary';
    routeFilter = params.get('route') || 'all';
    windowMinutes = [15,60,180,360,1440].includes(Number(params.get('minutes'))) ? Number(params.get('minutes')) : 60;
    endTime = params.get('at') || '';
    if (endTime && !Number.isFinite(Date.parse(endTime))) endTime = '';
    experimentMode = params.get('experiment') === '1';
    receptionStats = baselineStats = sampleInfo = baselineInfo = null;

    app.innerHTML = '<div class="compare-page">' +
      '<div class="page-header">' +
        '<a href="#/observers" class="btn-icon" title="Back to Observers" aria-label="Back">\u2190</a>' +
        '<h2>\uD83D\uDD0D Observer Comparison</h2>' +
      '</div>' +
      '<nav data-role="compare-breadcrumbs" aria-label="Compare breadcrumbs" class="compare-breadcrumbs"></nav>' +
      '<div id="compareControls" class="compare-controls"><div class="text-center text-muted" style="padding:20px">Loading observers\u2026</div></div>' +
      '<div id="compareContent"></div>' +
    '</div>';

    // #209 — Keyboard accessibility for compare table rows
    app.addEventListener('keydown', function (e) {
      var row = e.target.closest('tr[data-action="navigate"]');
      if (!row) return;
      if (e.key !== 'Enter' && e.key !== ' ') return;
      e.preventDefault();
      location.hash = row.dataset.value;
    });

    loadObservers();
  }

  function destroy() {
    requestGeneration++;
    observers = [];
    selA = null;
    selB = null;
    comparisonResult = null;
    packetsA = [];
    packetsB = [];
    routeFilter = 'all';
  }

  // #1646 round-2 — single shared "should we run a comparison?" predicate
  // used by every auto-run call site so guards cannot drift apart.
  // URL-prepopulated ?a=X&b=X (same observer in both slots) returns false.
  function isComparisonReady() {
    return !!(selA && selB && selA !== selB);
  }

  async function loadObservers() {
    try {
      var data = await api('/observers', { ttl: CLIENT_TTL.observers });
      observers = (data.observers || []).sort(function (a, b) {
        return (a.name || a.id).localeCompare(b.name || b.id);
      });
      renderControls();
      if (isComparisonReady()) runComparison();
    } catch (e) {
      document.getElementById('compareControls').innerHTML =
        '<div class="text-muted" style="padding:20px">Error loading observers: ' + escapeHtml(e.message) + '</div>';
    }
  }

  function renderControls() {
    var el = document.getElementById('compareControls');
    if (!el) return;

    var optionsHtml = '<option value="">Select observer\u2026</option>' +
      observers.map(function (o) {
        var label = escapeHtml(o.name || o.id);
        var region = o.iata ? ' (' + escapeHtml(o.iata) + ')' : '';
        return '<option value="' + escapeHtml(o.id) + '">' + label + region + '</option>';
      }).join('');

    el.innerHTML =
      '<div class="compare-selector">' +
        '<div class="compare-select-group">' +
          '<label for="compareObsA">Observer A</label>' +
          '<span class="compare-select-id" aria-hidden="true">A</span>' +
          '<select id="compareObsA" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<span class="compare-vs">vs</span>' +
        '<div class="compare-select-group">' +
          '<label for="compareObsB">Observer B</label>' +
          '<span class="compare-select-id" aria-hidden="true">B</span>' +
          '<select id="compareObsB" class="compare-select">' + optionsHtml + '</select>' +
        '</div>' +
        '<div class="compare-select-group">' +
          '<label for="compareRouteFilter">Packet Type</label>' +
          '<select id="compareRouteFilter" class="compare-select">' +
            '<option value="all">All packets</option>' +
            '<option value="flood">Flood only</option>' +
            '<option value="direct">Direct only</option>' +
          '</select>' +
        '</div>' +
      '</div>' +
      '<div class="compare-window-controls">' +
        '<label>Window <select id="compareMinutes"><option value="15">15 minutes</option><option value="60">1 hour</option><option value="180">3 hours</option><option value="360">6 hours</option><option value="1440">24 hours</option></select></label>' +
        '<label>End time / change time (local) <input id="compareAt" type="datetime-local"></label>' +
        '<label><input type="checkbox" id="compareExperiment"> Compare equal windows before and after this change</label>' +
        '<button class="btn" id="compareRefresh">Compare</button>' +
      '</div>' +
      '<details class="compare-notes"><summary>Private experiment markers, this browser only</summary>' +
        '<p>Record an antenna, filter or LNA change. Notes are not uploaded, shared or included in links.</p>' +
        '<label>Note <input id="compareNote" maxlength="160" placeholder="What changed?"></label> <button class="btn" id="compareSaveNote">Save marker at change time</button>' +
        '<div id="compareNotes"></div><p id="compareNoteStatus" role="status"></p></details>';

    document.getElementById('compareMinutes').value = String(windowMinutes);
    document.getElementById('compareExperiment').checked = experimentMode;
    if (endTime) {
      var d = new Date(endTime); d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
      document.getElementById('compareAt').value = d.toISOString().slice(0,16);
    }
    document.getElementById('compareRefresh').addEventListener('click', function() {
      windowMinutes = Number(document.getElementById('compareMinutes').value);
      var at = document.getElementById('compareAt').value;
      endTime = at ? new Date(at).toISOString() : '';
      experimentMode = document.getElementById('compareExperiment').checked;
      if (experimentMode && !endTime) { document.getElementById('compareContent').textContent = 'Choose a change time for the before/after comparison.'; return; }
      runComparison();
    });
    document.getElementById('compareSaveNote').addEventListener('click', saveMarker);
    renderMarkers();

    var ddA = document.getElementById('compareObsA');
    var ddB = document.getElementById('compareObsB');

    if (selA) ddA.value = selA;
    if (selB) ddB.value = selB;

    var ddRoute = document.getElementById('compareRouteFilter');
    ddRoute.value = routeFilter;
    ddRoute.addEventListener('change', function () {
      routeFilter = ddRoute.value;
      if (comparisonResult) runComparison();
    });

    // #1646 — single source of truth for "should we run a comparison?".
    // Called from change handlers and from the initial pre-populated
    // path in loadObservers(). The picker collapse rule (.is-collapsed)
    // is the ONLY DOM hook for state — no parallel data-collapsed attr.
    function updateBtn() {
      selA = ddA.value || null;
      selB = ddB.value || null;
      var wrap = document.getElementById('compareControls');
      var ready = isComparisonReady();
      if (wrap) wrap.classList.toggle('is-collapsed', ready);
      renderBreadcrumbs();
    }
    function onChange() {
      requestGeneration++;
      updateBtn();
      // change events only fire when value actually changes, so any
      // ready transition that lands here came from a real user action.
      if (isComparisonReady()) runComparison();
    }
    ddA.addEventListener('change', onChange);
    ddB.addEventListener('change', onChange);
    updateBtn();
  }

  // #1640 — render breadcrumbs linking back to each observer's detail page.
  // Hidden when neither observer is picked; otherwise:
  //   "Observers › <A name> ⇆ <B name>"
  // The "&lrm;" entities keep punctuation LTR in RTL contexts.
  function renderBreadcrumbs() {
    var el = document.querySelector('[data-role="compare-breadcrumbs"]');
    if (!el) return;
    function linkFor(id) {
      if (!id) return null;
      var match = null;
      for (var i = 0; i < observers.length; i++) {
        if (String(observers[i].id) === String(id)) { match = observers[i]; break; }
      }
      var label = match ? (match.name || match.id) : id;
      return '<a href="#/observers/' + encodeURIComponent(id) + '">' + escapeHtml(label) + '</a>';
    }
    var parts = ['<a href="#/observers">Observers</a>'];
    var aLink = linkFor(selA);
    var bLink = linkFor(selB);
    if (aLink || bLink) {
      var pair = [];
      if (aLink) pair.push(aLink);
      if (bLink) pair.push(bLink);
      parts.push(pair.join(' <span aria-hidden="true">\u21C6</span> '));
    }
    el.innerHTML = parts.join(' <span aria-hidden="true">\u203A</span> ');
  }

  function readMarkers() {
    try { return cleanExperimentNotes(JSON.parse(localStorage.getItem(NOTES_KEY) || '[]')); } catch (_) { return []; }
  }
  function saveMarker() {
    var status = document.getElementById('compareNoteStatus');
    var name = document.getElementById('compareNote').value.trim();
    var at = document.getElementById('compareAt').value;
    if (!name || !at || !isComparisonReady()) { status.textContent = 'Choose two observers, a change time and a note first.'; return; }
    try {
      localStorage.setItem(NOTES_KEY, JSON.stringify(cleanExperimentNotes(readMarkers().concat([{name:name,at:new Date(at).toISOString(),a:selA,b:selB}]))));
      status.textContent = 'Saved only in this browser. Clearing site data removes these notes.'; renderMarkers();
    } catch (_) { status.textContent = 'Browser storage is unavailable. Marker was not saved.'; }
  }
  function renderMarkers() {
    var el=document.getElementById('compareNotes'); if (!el) return;
    var notes=readMarkers();
    el.replaceChildren();
    notes.slice().reverse().forEach(function(n,index) {
      var row=document.createElement('p'), use=document.createElement('button'), remove=document.createElement('button');
      use.className=remove.className='btn';
      use.textContent=new Date(n.at).toLocaleString()+' '+n.name;
      use.addEventListener('click',function() {
        selA=n.a;selB=n.b;endTime=n.at;experimentMode=true;renderControls();runComparison();
      });
      remove.textContent='Remove';
      remove.addEventListener('click',function() {
        try { var current=readMarkers(); current.splice(notes.length-1-index,1); localStorage.setItem(NOTES_KEY,JSON.stringify(current));renderMarkers(); }
        catch (_) { document.getElementById('compareNoteStatus').textContent='Could not remove marker.'; }
      });
      row.append(use,remove);el.appendChild(row);
    });
  }
  function comparisonRequest(start,end) {
    return api('/observer-comparison?a='+encodeURIComponent(selA)+'&b='+encodeURIComponent(selB)+'&since='+encodeURIComponent(start.toISOString())+'&until='+encodeURIComponent(end.toISOString()), {ttl:10000});
  }
  function selectedRows(result,id) {
    return filterPacketsByRoute((result.packets || []).filter(function(p){return p.observer_id===id;}),routeFilter);
  }
  async function runComparison() {
    if (!isComparisonReady()) return;
    var content=document.getElementById('compareContent');if (!content) return;
    var generation=++requestGeneration;
    content.textContent='Loading reception sample…';
    var at=endTime ? new Date(endTime) : new Date(), duration=windowMinutes*60000;
    var start=experimentMode ? at : new Date(at.getTime()-duration);
    var end=experimentMode ? new Date(at.getTime()+duration) : at;
    if (end.getTime()>Date.now()+1000) { content.textContent='The complete after window is not available yet. Choose a shorter window or an earlier change time.';return; }
    var params=new URLSearchParams({a:selA,b:selB,minutes:String(windowMinutes),route:routeFilter});
    if (endTime) params.set('at',endTime);
    if (experimentMode) params.set('experiment','1');
    if (location.hash.split('?')[0]==='#/compare') history.replaceState(null,'','#/compare?'+params.toString());
    try {
      var result=await comparisonRequest(start,end);
      if(generation!==requestGeneration)return;
      packetsA=selectedRows(result,selA);packetsB=selectedRows(result,selB);
      sampleInfo=result;baselineInfo=baselineStats=null;
      receptionStats=computeReceptionStats(packetsA,packetsB);
      if(experimentMode) {
        var before=await comparisonRequest(new Date(at.getTime()-duration),at);
        if(generation!==requestGeneration)return;
        baselineInfo=before;baselineStats=computeReceptionStats(selectedRows(before,selA),selectedRows(before,selB));
      }
      comparisonResult=comparePacketSets(new Set(packetsA.map(function(p){return p.hash;})),new Set(packetsB.map(function(p){return p.hash;})));
      comparisonResult.packetMapA=new Map();comparisonResult.packetMapB=new Map();
      packetsA.forEach(function(p){comparisonResult.packetMapA.set(p.hash,p);});
      packetsB.forEach(function(p){comparisonResult.packetMapB.set(p.hash,p);});
      currentView='summary';renderComparison();
    } catch(e) { if(generation===requestGeneration) content.textContent='Comparison unavailable: '+e.message; }
  }
  function receptionSummary() {
    if(!receptionStats || !sampleInfo)return '';
    function table(stats,title) {
      var pair=stats.paired;
      var rows=[['Stored receptions',stats.a.receptions,stats.b.receptions],['Distinct message hashes',stats.a.messages,stats.b.messages],['Additional relay paths',stats.a.alternatePaths,stats.b.alternatePaths],['Distinct stored frames',stats.a.frames,stats.b.frames],['Receptions without stored bytes',stats.a.missingRaw,stats.b.missingRaw]];
      return '<h3>'+title+'</h3><div class="table-scroll"><table><thead><tr><th>Measure</th><th>A</th><th>B, reference</th></tr></thead><tbody>'+rows.map(function(r){return '<tr><th>'+r[0]+'</th><td>'+r[1].toLocaleString()+'</td><td>'+r[2].toLocaleString()+'</td></tr>';}).join('')+'</tbody></table></div>'+
        '<p>Paired SNR, A minus B: <strong>'+(pair.median==null?'Not available':pair.median.toFixed(1)+' dB')+'</strong>. '+pair.count+' one-to-one identical-frame pairs within 10 seconds.'+(pair.count<30?' Small sample; do not infer an improvement.':'')+'</p>';
    }
    var partial=sampleInfo.truncated || (baselineInfo && baselineInfo.truncated);
    var html='<section class="compare-receptions"><p>'+escapeHtml(new Date(sampleInfo.since).toLocaleString())+' to '+escapeHtml(new Date(sampleInfo.until).toLocaleString())+'</p>'+
      '<p class="text-muted">On-demand sample. Up to '+sampleInfo.observer_cap.toLocaleString()+' receptions per observer from the newest '+sampleInfo.scan_cap.toLocaleString()+' site observations in the window. TX reports excluded. No stored bytes means no SNR pairing; flood/direct filters omit records without a reception header.</p>'+
      (partial?'<p class="compare-warning"><strong>Sample capped. Reception totals are lower bounds; overlap and exclusives may be misleading. These windows cannot establish improvement. Shorten the window.</strong></p>':'<p>Full stored reception sample for this window. This does not prove continuous observer uptime or complete MQTT delivery.</p>');
    if(baselineStats) {
      html+=table(baselineStats,'Before change')+table(receptionStats,'After change');
      var oldRatio=baselineStats.b.receptions ? baselineStats.a.receptions/baselineStats.b.receptions : null;
      var newRatio=receptionStats.b.receptions ? receptionStats.a.receptions/receptionStats.b.receptions : null;
      html+='<p>A receptions per 100 at reference B: '+(oldRatio==null?'unavailable':(oldRatio*100).toFixed(1))+' before; '+(newRatio==null?'unavailable':(newRatio*100).toFixed(1))+' after. A reference helps account for changing traffic; different frequencies, uptime, TX activity and propagation still confound this comparison.</p>';
    } else html+=table(receptionStats,'Reception comparison');
    return html+'<p>Compare observers on the same RF settings. Additional paths count distinct recorded paths beyond the first for each message, not duplicate uploads. Stored receptions include retained repeat observations; ingest deduplication may have collapsed identical reports. Message hashes count each message once.</p></section>';
  }

  function obsName(id) {
    for (var i = 0; i < observers.length; i++) {
      if (observers[i].id === id) return observers[i].name || id;
    }
    return id ? id.substring(0, 12) : 'Unknown';
  }

  function renderComparison() {
    var content = document.getElementById('compareContent');
    if (!content || !comparisonResult) return;

    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));
    var total = r.onlyA.length + r.onlyB.length + r.both.length;
    var pctBoth = total > 0 ? Math.round(r.both.length / total * 100) : 0;
    var pctA = total > 0 ? Math.round(r.onlyA.length / total * 100) : 0;
    var pctB = total > 0 ? Math.round(r.onlyB.length / total * 100) : 0;

    // Type breakdown for "both" packets
    var typeBreakdown = {};
    r.both.forEach(function (h) {
      var p = r.packetMapA.get(h) || r.packetMapB.get(h);
      if (p) {
        var t = p.payload_type;
        typeBreakdown[t] = (typeBreakdown[t] || 0) + 1;
      }
    });

    var typeHtml = Object.keys(typeBreakdown).map(function (t) {
      return '<span class="compare-type-badge">' +
        escapeHtml(PAYLOAD_LABELS[t] || 'Type ' + t) + ' <b>' + typeBreakdown[t] + '</b>' +
      '</span>';
    }).join('');

    var stats = computeOverlapStats(r);

    content.innerHTML =
      '<div class="compare-results">' + receptionSummary() +
        // Headline strip — A | shared | B above a single proportional bar.
        // All three cells lead with their percentage so the row reads in
        // one unit (Tufte: show data variation, not design variation).
        // #1646
        '<section class="compare-strip" aria-label="Packet overlap summary">' +
          '<div class="compare-strip-row">' +
            '<div class="compare-strip-side" data-view="onlyA" role="button" tabindex="0" aria-label="Show only ' + nameA + ' packets">' +
              '<div class="compare-strip-name">' + nameA + '</div>' +
              '<div class="compare-strip-side-pct">' + pctA + '<span class="compare-strip-side-pct-unit">%</span></div>' +
              '<div class="compare-strip-sub">' + r.onlyA.length.toLocaleString() + ' only here</div>' +
            '</div>' +
            '<div class="compare-strip-mid" data-view="both" role="button" tabindex="0" aria-label="Show shared packets">' +
              '<div class="compare-strip-mid-pct">' + pctBoth + '<span class="compare-strip-mid-pct-unit">%</span></div>' +
              '<div class="compare-strip-mid-count">' + r.both.length.toLocaleString() + '</div>' +
              '<div class="compare-strip-mid-label">of all unique</div>' +
            '</div>' +
            '<div class="compare-strip-side compare-strip-side-b" data-view="onlyB" role="button" tabindex="0" aria-label="Show only ' + nameB + ' packets">' +
              '<div class="compare-strip-name">' + nameB + '</div>' +
              '<div class="compare-strip-side-pct">' + pctB + '<span class="compare-strip-side-pct-unit">%</span></div>' +
              '<div class="compare-strip-sub">' + r.onlyB.length.toLocaleString() + ' only here</div>' +
            '</div>' +
          '</div>' +
          // Single shared-axis diff bar. Width is exact proportion.
          '<div class="compare-bar-container">' +
            '<div class="compare-bar" role="img"' +
                ' aria-label="' + nameA + ' only ' + pctA + '%, both ' + pctBoth + '%, ' + nameB + ' only ' + pctB + '%">' +
              (pctA > 0 ? '<div class="compare-bar-seg compare-bar-a" style="width:' + pctA + '%" title="Only ' + nameA + ': ' + r.onlyA.length + '"></div>' : '') +
              (pctBoth > 0 ? '<div class="compare-bar-seg compare-bar-both" style="width:' + pctBoth + '%" title="Both: ' + r.both.length + '"></div>' : '') +
              (pctB > 0 ? '<div class="compare-bar-seg compare-bar-b" style="width:' + pctB + '%" title="Only ' + nameB + ': ' + r.onlyB.length + '"></div>' : '') +
            '</div>' +
            '<div class="compare-bar-legend">' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-a"></span> ' + nameA + ' only</span>' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-both"></span> Both</span>' +
              '<span class="compare-legend-item"><span class="compare-dot compare-dot-b"></span> ' + nameB + ' only</span>' +
            '</div>' +
          '</div>' +
        '</section>' +

        // Asymmetric reach — two compact sentences instead of two big cards
        '<section class="compare-asym" aria-label="Directional reach">' +
          '<div class="compare-asym-line">' +
            '<span class="compare-asym-pct">' + stats.aSeesOfB.toFixed(1) + '%</span>' +
            nameA + ' saw <b>' + stats.shared.toLocaleString() + '</b> of ' + nameB +
            '\u2019s <b>' + stats.totalB.toLocaleString() + '</b> packets' +
          '</div>' +
          '<div class="compare-asym-line">' +
            '<span class="compare-asym-pct">' + stats.bSeesOfA.toFixed(1) + '%</span>' +
            nameB + ' saw <b>' + stats.shared.toLocaleString() + '</b> of ' + nameA +
            '\u2019s <b>' + stats.totalA.toLocaleString() + '</b> packets' +
          '</div>' +
        '</section>' +

        // Shared packet types — pills, mono-numeric
        (typeHtml ? '<section class="compare-type-summary" aria-label="Shared packet types">' +
            '<span class="compare-type-summary-label">Shared types</span>' + typeHtml +
          '</section>' : '') +

        // Detail tabs
        '<div class="compare-tabs" role="tablist">' +
          '<button class="tab-btn' + (currentView === 'summary' ? ' active' : '') + '" data-cview="summary" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'summary' ? 'true' : 'false') + '" tabindex="' + (currentView === 'summary' ? '0' : '-1') + '">Summary</button>' +
          '<button class="tab-btn' + (currentView === 'both' ? ' active' : '') + '" data-cview="both" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'both' ? 'true' : 'false') + '" tabindex="' + (currentView === 'both' ? '0' : '-1') + '">Both (' + r.both.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyA' ? ' active' : '') + '" data-cview="onlyA" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'onlyA' ? 'true' : 'false') + '" tabindex="' + (currentView === 'onlyA' ? '0' : '-1') + '">Only ' + nameA + ' (' + r.onlyA.length + ')</button>' +
          '<button class="tab-btn' + (currentView === 'onlyB' ? ' active' : '') + '" data-cview="onlyB" role="tab" aria-controls="compareDetail" aria-selected="' + (currentView === 'onlyB' ? 'true' : 'false') + '" tabindex="' + (currentView === 'onlyB' ? '0' : '-1') + '">Only ' + nameB + ' (' + r.onlyB.length + ')</button>' +
        '</div>' +
        '<div id="compareDetail"></div>' +
      '</div>';

    // Sync the tablist's active/aria-selected/tabindex to currentView.
    function syncTabState() {
      content.querySelectorAll('.tab-btn').forEach(function (b) {
        var on = b.dataset.cview === currentView;
        b.classList.toggle('active', on);
        b.setAttribute('aria-selected', on ? 'true' : 'false');
        b.setAttribute('tabindex', on ? '0' : '-1');
      });
    }

    // Activate a [data-view] strip segment OR a [data-cview] tab.
    function activate(el) {
      if (!el) return;
      if (el.dataset.cview) {
        currentView = el.dataset.cview;
      } else if (el.dataset.view) {
        currentView = el.dataset.view;
      } else {
        return;
      }
      syncTabState();
      renderDetail();
    }

    // Bind tab clicks + strip clicks
    content.onclick = function(e) {
      var btn = e.target.closest('[data-cview]');
      if (btn) { activate(btn); return; }
      var seg = e.target.closest('[data-view]');
      if (seg) { activate(seg); }
    };

    // Keyboard activation for tabs (arrow nav) + strip segments (Enter/Space).
    content.addEventListener('keydown', function (e) {
      var tab = e.target.closest('[data-cview]');
      if (tab) {
        if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
          e.preventDefault();
          var tabs = Array.prototype.slice.call(content.querySelectorAll('.tab-btn'));
          var idx = tabs.indexOf(tab);
          if (idx < 0) return;
          var next = e.key === 'ArrowRight'
            ? tabs[(idx + 1) % tabs.length]
            : tabs[(idx - 1 + tabs.length) % tabs.length];
          activate(next);
          next.focus();
          return;
        }
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          activate(tab);
          return;
        }
      }
      var seg = e.target.closest('[data-view]');
      if (seg && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault();
        activate(seg);
      }
    });

    renderDetail();
  }

  function renderDetail() {
    var el = document.getElementById('compareDetail');
    if (!el || !comparisonResult) return;
    var r = comparisonResult;
    var nameA = escapeHtml(obsName(selA));
    var nameB = escapeHtml(obsName(selB));

    if (currentView === 'summary') {
      // Textual summary — the headline strip + asym lines already cover
      // the quantitative story; this paragraph adds context and surfaces
      // edge cases (no shared packets, perfect overlap).
      var total = r.onlyA.length + r.onlyB.length + r.both.length;
      var overlap = total > 0 ? (r.both.length / total * 100).toFixed(1) : '0.0';
      el.innerHTML =
        '<div class="compare-summary-text">' +
          '<p>In the selected reception sample, <strong>' + nameA + '</strong> saw <strong>' +
            (r.onlyA.length + r.both.length).toLocaleString() + '</strong> unique packets ' +
          'and <strong>' + nameB + '</strong> saw <strong>' +
            (r.onlyB.length + r.both.length).toLocaleString() + '</strong> unique packets. ' +
          '<strong>' + r.both.length.toLocaleString() + '</strong> (' + overlap + '%) were seen by both observers.</p>' +
          (r.both.length === 0 && total > 0 ? '<p class="compare-warning">\u26A0\uFE0F These observers share no packets \u2014 they may be on different frequencies or too far apart.</p>' : '') +
          (r.onlyA.length === 0 && r.onlyB.length === 0 && r.both.length > 0 ? '<p class="compare-good">\u2705 Perfect overlap \u2014 both observers see the same packets.</p>' : '') +
        '</div>';
      return;
    }

    var hashes = r[currentView] || [];
    if (hashes.length === 0) {
      el.innerHTML = '<div class="text-muted" style="padding:20px">No packets in this category.</div>';
      return;
    }

    // Show up to 200 packets in the table
    var displayLimit = 200;
    var displayed = hashes.slice(0, displayLimit);
    var mapA = r.packetMapA;
    var mapB = r.packetMapB;

    el.innerHTML =
      (hashes.length > displayLimit ? '<div class="text-muted" style="margin-bottom:8px">Showing first ' + displayLimit + ' of ' + hashes.length.toLocaleString() + ' packets.</div>' : '') +
      '<div class="analytics-table-scroll"><table class="data-table compare-table">' +
        '<thead><tr>' +
          '<th scope="col">Hash</th><th scope="col">Time</th><th scope="col">Type</th><th scope="col">Observer</th>' +
        '</tr></thead>' +
        '<tbody>' + displayed.map(function (h) {
          var p = mapA.get(h) || mapB.get(h);
          if (!p) return '';
          var typeName = PAYLOAD_LABELS[p.payload_type] || 'Type ' + p.payload_type;
          var obsLabel = '';
          if (currentView === 'both') {
            obsLabel = nameA + ', ' + nameB;
          } else if (currentView === 'onlyA') {
            obsLabel = nameA;
          } else {
            obsLabel = nameB;
          }
          return '<tr style="cursor:pointer" tabindex="0" role="row" data-action="navigate" data-value="#/packets/' + escapeHtml(h) + '" onclick="location.hash=\'#/packets/' + escapeHtml(h) + '\'">' +
            '<td class="mono" style="font-size:0.85em">' + escapeHtml(h.substring(0, 12)) + '</td>' +
            '<td>' + timeAgo(typeof p.timestamp === 'number' ? new Date(p.timestamp * 1000).toISOString() : (p.timestamp || p.first_seen)) + '</td>' +
            '<td><span class="payload-badge badge-' + payloadTypeColor(p.payload_type) + '">' + escapeHtml(typeName) + '</span></td>' +
            '<td>' + obsLabel + '</td>' +
          '</tr>';
        }).join('') +
        '</tbody>' +
      '</table></div>';
  }

  registerPage('compare', { init: init, destroy: destroy });
})();
