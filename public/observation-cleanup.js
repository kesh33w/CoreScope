/* Bulk cleanup is an operator action; no credentials or captured packets are stored here. */
(function (root) {
  'use strict';
  var base = '/api/admin/observation-cleanup';
  var activeDispose = null;
  function render(active) {
    return '<div class="cust-panel' + (active ? ' active' : '') + '" data-panel="cleanup">' +
      '<p class="cust-section-title">Delete bad observations in bulk</p>' +
      '<p>Find an observer, preview matching history, then delete it in one recoverable bulk operation.</p>' +
      '<div class="cust-field"><label for="cleanup-search">Find observer by name or ID</label><input id="cleanup-search" type="search" maxlength="128" placeholder="For example, AB0OO" autocomplete="off"></div>' +
      '<p id="cleanup-search-status" role="status"></p><div id="cleanup-results"></div>' +
      '<p id="cleanup-selected" aria-live="polite">No observer selected.</p>' +
      '<details><summary>Advanced: enter an exact observer ID</summary><div class="cust-field"><label for="cleanup-observer">Exact observer ID</label><input id="cleanup-observer" type="text" maxlength="256" autocomplete="off"></div></details>' +
      '<p id="cleanup-status" role="status">Loading cleanup availability…</p>' +
      '<fieldset id="cleanup-fields" disabled style="border:0;padding:0">' +
      '<div class="cust-field"><label for="cleanup-iata">Current observer IATA (optional)</label><input id="cleanup-iata" type="text" maxlength="3" placeholder="SMF"></div>' +
      '<div class="cust-field"><label for="cleanup-from">From (local time, optional)</label><input id="cleanup-from" type="datetime-local"></div>' +
      '<div class="cust-field"><label for="cleanup-to">To (local time, optional)</label><input id="cleanup-to" type="datetime-local"></div>' +
      '<div class="cust-field"><label for="cleanup-key">Server API key (or sign in as admin)</label><input id="cleanup-key" type="password" autocomplete="off"></div>' +
      '<button id="cleanup-preview">Preview matching observations</button>' +
      '<div id="cleanup-summary" aria-live="polite"></div>' +
      '<button id="cleanup-delete" hidden>Delete previewed observations</button>' +
      '<p id="cleanup-job"></p>' +
      '<div class="cust-field"><label for="cleanup-restore-id">Cleanup request ID to restore</label><input id="cleanup-restore-id" type="text" maxlength="64" autocomplete="off"></div>' +
      '<button id="cleanup-check">Check request status</button> <button id="cleanup-restore">Restore archived observations</button>' +
      '</fieldset><details><summary>What this removes and important limits</summary><p><strong>Historical frequency is not recorded reliably.</strong> Current region or radio settings do not prove an old observation’s frequency. Choose the known affected observer and period. The IATA filter uses current observer metadata.</p><p>Other observers’ observations are preserved. Node registry entries, lifetime aggregates and shared advert-route evidence remain. Archives occupy disk until separately managed.</p><p>Restart all CoreScope servers reading this database after deletion or restoration to refresh in-memory history. Deleting history does not block new observations; retain the appropriate blacklist or frequency filter.</p></details></div>';

  }
  function selectionFromValues(observer, iata, from, to) {
    observer = observer.trim(); iata = iata.trim().toUpperCase();
    if (!observer) throw new Error('Find and select an observer, or enter its exact ID under Advanced.');
    if (iata && !/^[A-Z]{3}$/.test(iata)) throw new Error('IATA must contain three letters.');
    function epoch(value) { if (!value) return 0; var n = Date.parse(value); if (!Number.isFinite(n)) throw new Error('Enter valid date/time values.'); return Math.floor(n / 1000); }
    var start = epoch(from), end = epoch(to);
    if (start && end && start > end) throw new Error('From must be before To.');
    return { observerId: observer, iata: iata, from: start, to: end };
  }
  function saveJob(id) { try { root.localStorage.setItem('corescope-cleanup-last-job', id); } catch (_) {} }
  function lastJob() { try { return root.localStorage.getItem('corescope-cleanup-last-job') || ''; } catch (_) { return ''; } }
  function dispose() { if (activeDispose) activeDispose(); activeDispose = null; }
  function init(container) {
    dispose();
    var q = function (id) { return container.querySelector('#cleanup-' + id); };
    var status = q('status'), fields = q('fields');
    if (!status || !fields) return;
    var controller = new AbortController(), timer = null, disposed = false, busy = false, snapshot = null, revision = 0, searchTimer = null, searchRevision = 0;
    activeDispose = function () { disposed = true; controller.abort(); clearTimeout(timer); clearTimeout(searchTimer); var key = q('key'); if (key) key.value = ''; };
    function live() { return !disposed && status.isConnected; }
    function say(s) { if (live()) status.textContent = s; }
    function params() { return new URLSearchParams((root.location.hash.split('?')[1] || '')); }
    function setParams(values) {
      var p = params(); Object.keys(values).forEach(function (k) { if (values[k]) p.set(k, values[k]); else p.delete(k); });
      var path = root.location.hash.split('?')[0] || '#/home'; root.history.replaceState(null, '', path + (p.toString() ? '?' + p.toString() : ''));
    }
    var p = params(); ['observer','iata','from','to'].forEach(function (k) { q(k).value = p.get('cleanup_' + k) || ''; });
    if (q('observer').value) q('selected').textContent = 'Selected observer ID: ' + q('observer').value;
    var previousJob = p.get('cleanup_job') || lastJob();
    q('restore-id').value = previousJob;
    function headers() {
      var h = { 'Content-Type': 'application/json' };
      if (root.CSAuth && root.CSAuth.isAdmin()) Object.assign(h, root.CSAuth.adminHeaders());
      else { var key = q('key').value; if (!key) throw new Error('Sign in as an admin or enter the server API key.'); h['X-API-Key'] = key; }
      return h;
    }
    function request(path, method, body, auth) {
      return Promise.resolve().then(function () { return fetch(base + path, {method: method, cache:'no-store', signal:(path === '' || path === '/restore') ? undefined : controller.signal, headers:auth ? headers() : {'Content-Type':'application/json'}, body:body == null ? undefined : JSON.stringify(body)}).then(function (r) {
        return r.json().then(function (data) { if (!r.ok) throw new Error(data.error || 'HTTP ' + r.status); return data; });
      }); });
    }
    function setBusy(v) { busy = v; if (!live()) return; q('preview').disabled = v; q('delete').disabled = v; q('restore').disabled = v; q('check').disabled = v; }
    function fail(err) { if (err.name !== 'AbortError') say(err.message); }
    function clearSnapshot() { revision++; snapshot = null; q('summary').textContent = ''; q('delete').hidden = true; }
    ['observer','iata','from','to'].forEach(function (k) { q(k).addEventListener('input', function () { clearSnapshot(); if (k === 'observer') q('selected').textContent = q(k).value ? 'Selected observer ID: ' + q(k).value : 'No observer selected.'; var values = {}; values['cleanup_' + k] = q(k).value; setParams(values); }); });
    function findObservers() {
      var query = q('search').value.trim(), version = ++searchRevision;
      q('results').replaceChildren();
      if (query.length < 2) { q('search-status').textContent = 'Enter at least two characters. Search includes blocked observers and retained history.'; return; }
      q('search-status').textContent = 'Finding observers…';
      request('/observers?q=' + encodeURIComponent(query), 'GET', null, false).then(function (data) {
        if (!live() || version !== searchRevision) return;
        var rows = data.observers || [];
        q('search-status').textContent = rows.length ? rows.length + ' matches' + (data.truncated ? '; refine your search to see more.' : '.') : 'No matching observers in retained history.';
        rows.forEach(function (observer) {
          var button = document.createElement('button'); button.type = 'button';
          button.style.cssText = 'display:block;text-align:left;width:100%;margin-bottom:6px;overflow-wrap:anywhere;white-space:normal';
          var label = (observer.name || 'Unnamed observer') + ' · ' + (observer.iata || 'Unknown region') + ' · ' + Number(observer.observations || 0).toLocaleString() + ' observations' + (observer.blocked ? ' · Blocked' : '') + ' · ID: ' + observer.id;
          button.textContent = label;
          button.addEventListener('click', function () {
            clearSnapshot(); q('observer').value = observer.id;
            q('selected').textContent = 'Selected: ' + label;
            setParams({cleanup_observer:observer.id});
            q('results').replaceChildren(); q('search-status').textContent = 'Observer selected. Set an optional time range, then preview.';
          });
          q('results').appendChild(button);
        });
      }).catch(function (err) { if (live() && version === searchRevision && err.name !== 'AbortError') q('search-status').textContent = 'Could not search observers: ' + err.message; });
    }
    q('search').addEventListener('input', function () { clearTimeout(searchTimer); searchRevision++; q('results').replaceChildren(); searchTimer = setTimeout(findObservers, 200); });
    findObservers();
    function poll(id) {
      if (!live()) return;
      request('/status?id=' + encodeURIComponent(id), 'GET', null, true).then(function (result) {
        if (!live()) return;
        if (result.status === 'pending' || result.status === 'running') { say('Cleanup request ' + id + ' is ' + result.status + '.'); timer = setTimeout(function () { poll(id); }, 1500); return; }
        setBusy(false);
        if (result.error || result.status === 'error') { q('restore-id').value = result.archiveId || id; say('Cleanup failed: ' + (result.error || 'unknown error') + '. Review the archive/result before retrying.' + (result.restartRequired ? ' Some batches committed. Restore using this archive if needed, and restart all CoreScope servers reading this database.' : '')); return; }
        say('Completed. ' + (result.restored > 0 ? Number(result.restored).toLocaleString() + ' observations restored.' : Number(result.deleted || 0).toLocaleString() + ' observations removed. Archive retained for recovery.') + ' Restart all CoreScope servers reading this database, then refresh packet views: its in-memory history is not updated until restart.');
        q('restore-id').value = result.archiveId || id;
      }).catch(function (err) { setBusy(false); fail(err); say('Could not check completion: ' + err.message + '. Request ID ' + id + ' is preserved; do not blindly repeat deletion.'); });
    }
    q('preview').addEventListener('click', function () {
      if (busy) return;
      var selection; try { selection = selectionFromValues(q('observer').value, q('iata').value, q('from').value, q('to').value); headers(); } catch (err) { fail(err); return; }
      clearSnapshot(); var previewRevision = revision; setBusy(true); say('Counting matching observations…');
      request('/preview', 'POST', selection, true).then(function (data) {
        if (!live()) return;
        if (revision !== previewRevision) { say('Selection changed. Preview again before deleting.'); return; }
        snapshot = data; snapshot.selection = data.selection || selection;
        q('summary').textContent = Number(data.count || 0).toLocaleString() + ' observations match this snapshot.' + (data.transmissions != null ? ' They touch ' + Number(data.transmissions).toLocaleString() + ' distinct transmissions; ' + Number(data.exclusiveTransmissions || 0).toLocaleString() + ' have no other retained observations.' : '') + ' New observations after the preview cutoff are excluded.';
        q('delete').hidden = !(data.count > 0); say(data.count ? 'Review the observer and time range, then confirm deletion.' : 'No matching observations.');
      }).catch(fail).finally(function () { setBusy(false); });
    });
    q('delete').addEventListener('click', function () {
      if (busy || !snapshot || !snapshot.count) return;
      if (!root.confirm('Delete ' + Number(snapshot.count).toLocaleString() + ' observations from observer ' + snapshot.selection.observerId + '? A recoverable archive will be retained; other observers are preserved.')) return;
      var body = {selection:snapshot.selection, maxObservationId:snapshot.maxObservationId, expectedCount:snapshot.count};
      setBusy(true); say('Submitting cleanup…');
      request('', 'POST', body, true).then(function (data) {
        var id = data.requestId; saveJob(id);
        if (!live()) return;
        clearSnapshot(); q('job').textContent = 'Request ID: ' + id; q('restore-id').value = id; setParams({cleanup_job:id}); poll(id);
      }).catch(function (err) { setBusy(false); clearSnapshot(); fail(err); });
    });
    q('check').addEventListener('click', function () {
      if (busy) return; var id = q('restore-id').value.trim();
      if (!/^[a-fA-F0-9]{1,64}$/.test(id)) { say('Enter a valid cleanup request ID.'); return; }
      try { headers(); } catch (err) { fail(err); return; }
      setBusy(true); poll(id);
    });
    q('restore').addEventListener('click', function () {
      if (busy) return;
      var id = q('restore-id').value.trim(); if (!/^[a-fA-F0-9]{1,64}$/.test(id)) { say('Enter a valid cleanup request ID.'); return; }
      if (!root.confirm('Restore archived observations from cleanup ' + id + '?')) return;
      setBusy(true); say('Submitting restore…');
      request('/restore', 'POST', {id:id}, true).then(function (data) {
        saveJob(data.requestId);
        if (!live()) return; q('job').textContent = 'Restore request ID: ' + data.requestId; setParams({cleanup_job:data.requestId}); poll(data.requestId);
      }).catch(function (err) { setBusy(false); fail(err); });
    });
    request('/capabilities', 'GET', null, false).then(function (data) {
      if (!live()) return;
      var admin = root.CSAuth && root.CSAuth.isAdmin();
      fields.disabled = !(data.writeEnabled || admin);
      say(fields.disabled ? 'Cleanup is unavailable: this server has no configured admin/API key. No observations have been changed.' : 'Ready. Preview and cleanup require your admin login or API key.');
      if (previousJob) q('job').textContent = 'Previous request ID: ' + previousJob + '. Use the archive ID to restore if needed.';
    }).catch(fail);
  }
  root.ObservationCleanup = {render:render,init:init,dispose:dispose,selectionFromValues:selectionFromValues};
  if (typeof module !== 'undefined' && module.exports) module.exports = root.ObservationCleanup;
})(typeof window !== 'undefined' ? window : globalThis);
