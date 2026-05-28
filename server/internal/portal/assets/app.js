// AutoDeploy portal — UI helpers.
//
// Pure vanilla, no framework. Every helper is opt-in via data-* attributes
// so server-rendered HTML stays in charge of the page; the JS just adds
// polish (sorting, filtering, copy-to-clipboard, theme toggle, log tail).
(function () {
  'use strict';

  // ---- Theme toggle --------------------------------------------------
  // The system preference applies by default; an explicit user pick
  // (saved in localStorage) overrides it via data-theme on <html>.
  function readTheme() {
    try { return localStorage.getItem('ad-theme') || ''; } catch (_) { return ''; }
  }
  function writeTheme(t) {
    try { localStorage.setItem('ad-theme', t); } catch (_) {}
  }
  function applyTheme(t) {
    if (t === 'light' || t === 'dark') {
      document.documentElement.setAttribute('data-theme', t);
    } else {
      document.documentElement.removeAttribute('data-theme');
    }
  }
  // Apply early (already done inline in _layout.html) but re-apply on
  // visibility change in case the OS swapped while we were hidden.
  applyTheme(readTheme());

  document.addEventListener('click', function (e) {
    const btn = e.target.closest('[data-theme-toggle]');
    if (!btn) return;
    e.preventDefault();
    const cur = document.documentElement.getAttribute('data-theme') || '';
    let next;
    if (cur === 'dark') next = 'light';
    else if (cur === 'light') next = ''; // back to system
    else next = 'dark';
    writeTheme(next);
    applyTheme(next);
    syncThemeIcons();
  });
  function syncThemeIcons() {
    const cur = document.documentElement.getAttribute('data-theme') || 'auto';
    document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
      b.setAttribute('aria-label', 'Theme: ' + (cur || 'auto') + ' (click to cycle)');
      b.querySelectorAll('[data-show]').forEach(function (el) {
        el.style.display = (el.getAttribute('data-show') === (cur || 'auto')) ? '' : 'none';
      });
    });
  }
  syncThemeIcons();

  // ---- Copy to clipboard --------------------------------------------
  document.addEventListener('click', function (e) {
    const btn = e.target.closest('.copy, [data-copy]');
    if (!btn) return;
    e.preventDefault();
    let text = btn.getAttribute('data-copy');
    if (!text) {
      const tgt = btn.previousElementSibling || btn.parentElement;
      text = tgt ? (tgt.innerText || tgt.textContent || '').trim() : '';
    }
    if (!text) return;
    const done = function () {
      const old = btn.innerHTML;
      btn.classList.add('copied');
      btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="20 6 9 17 4 12"/></svg>';
      setTimeout(function () {
        btn.classList.remove('copied');
        btn.innerHTML = old;
      }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text); done(); });
    } else {
      fallbackCopy(text);
      done();
    }
  });
  function fallbackCopy(text) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (_) {}
    document.body.removeChild(ta);
  }

  // ---- Sortable tables ----------------------------------------------
  // <table data-sortable>; first <thead> row's <th>s become sort toggles,
  // except those with data-nosort. Numeric columns mark themselves with
  // data-sort="num" or a class containing "num".
  document.querySelectorAll('table[data-sortable]').forEach(function (table) {
    const ths = table.querySelectorAll('thead th');
    ths.forEach(function (th, idx) {
      if (th.hasAttribute('data-nosort')) return;
      th.classList.add('sortable');
      th.addEventListener('click', function () { sortTable(table, idx, th); });
    });
  });
  function sortTable(table, col, th) {
    const tbody = table.tBodies[0];
    if (!tbody) return;
    const rows = Array.from(tbody.rows);
    const current = th.classList.contains('asc') ? 'asc' : (th.classList.contains('desc') ? 'desc' : '');
    const next = current === 'asc' ? 'desc' : 'asc';
    table.querySelectorAll('thead th').forEach(function (h) { h.classList.remove('asc', 'desc'); });
    th.classList.add(next);
    const numeric = th.getAttribute('data-sort') === 'num' || /\bnum\b/.test(th.className);
    rows.sort(function (a, b) {
      const av = cellValue(a, col), bv = cellValue(b, col);
      if (numeric) {
        const an = parseFloat(av.replace(/[^0-9.\-]/g, '')) || 0;
        const bn = parseFloat(bv.replace(/[^0-9.\-]/g, '')) || 0;
        return next === 'asc' ? an - bn : bn - an;
      }
      return next === 'asc' ? av.localeCompare(bv) : bv.localeCompare(av);
    });
    rows.forEach(function (r) { tbody.appendChild(r); });
  }
  function cellValue(row, col) {
    const c = row.cells[col];
    if (!c) return '';
    return (c.getAttribute('data-sort-value') || c.innerText || c.textContent || '').trim();
  }

  // ---- Filter-as-you-type -------------------------------------------
  // <input data-filter="#table-id">  -> hides table rows whose textContent
  // doesn't contain the (case-insensitive, whitespace-collapsed) query.
  document.querySelectorAll('input[data-filter]').forEach(function (inp) {
    const target = document.querySelector(inp.getAttribute('data-filter'));
    if (!target) return;
    inp.addEventListener('input', function () { applyFilter(target, inp.value); });
    // Apply once in case the URL hash carried a value.
    if (inp.value) applyFilter(target, inp.value);
  });
  function applyFilter(table, raw) {
    const q = raw.trim().toLowerCase();
    let matched = 0;
    table.querySelectorAll('tbody tr').forEach(function (tr) {
      if (!q) { tr.style.display = ''; matched++; return; }
      const hay = (tr.innerText || '').toLowerCase();
      const ok = hay.indexOf(q) !== -1;
      tr.style.display = ok ? '' : 'none';
      if (ok) matched++;
    });
    const counter = document.querySelector('[data-filter-count="' + table.id + '"]');
    if (counter) counter.textContent = matched;
  }

  // ---- Confirm-via-dialog -------------------------------------------
  // Forms with data-confirm="msg" open a styled <dialog> instead of the
  // native confirm prompt; individual <button data-confirm="msg"> get a
  // per-button prompt (useful for forms that have a benign Save and a
  // destructive Delete in the same actions row).
  let pendingForm = null;
  let pendingSubmitter = null;
  // Capture the submitter for a form before submit fires so we can read
  // its data-confirm.
  document.addEventListener('click', function (e) {
    const btn = e.target.closest('button[type="submit"], input[type="submit"]');
    if (btn && btn.form) btn.form._adSubmitter = btn;
  }, true);
  document.addEventListener('submit', function (e) {
    const form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (form.dataset.confirmed === '1') { delete form._adSubmitter; return; }
    const btn = (e.submitter || form._adSubmitter) || null;
    const msg = (btn && btn.getAttribute('data-confirm')) || form.getAttribute('data-confirm') || '';
    if (!msg) { delete form._adSubmitter; return; }
    e.preventDefault();
    if (typeof HTMLDialogElement !== 'function') {
      if (confirm(msg)) { form.dataset.confirmed = '1'; if (btn) { btn.click(); } else { form.submit(); } }
      return;
    }
    const dlg = ensureConfirmDialog();
    dlg.querySelector('[data-msg]').textContent = msg;
    pendingForm = form;
    pendingSubmitter = btn;
    dlg.showModal();
  });
  function ensureConfirmDialog() {
    let dlg = document.getElementById('ad-confirm');
    if (dlg) return dlg;
    dlg = document.createElement('dialog');
    dlg.id = 'ad-confirm';
    dlg.className = 'modal';
    dlg.innerHTML =
      '<div class="body"><h3>Please confirm</h3><p data-msg></p></div>' +
      '<div class="actions">' +
      '<button type="button" class="secondary" data-cancel>Cancel</button>' +
      '<button type="button" class="danger" data-ok>Continue</button>' +
      '</div>';
    document.body.appendChild(dlg);
    dlg.querySelector('[data-cancel]').addEventListener('click', function () {
      pendingForm = null; pendingSubmitter = null; dlg.close();
    });
    dlg.querySelector('[data-ok]').addEventListener('click', function () {
      const f = pendingForm; const btn = pendingSubmitter;
      pendingForm = null; pendingSubmitter = null; dlg.close();
      if (f) {
        f.dataset.confirmed = '1';
        if (btn) { btn.click(); } else { f.submit(); }
      }
    });
    return dlg;
  }

  // ---- Bulk-select on lists -----------------------------------------
  // <table data-bulk-target="/portal/foo/{id}/delete"> opts in. A
  // header checkbox toggles all body rows; a sticky bar appears with
  // the selection count and a "Delete selected" button that submits
  // one POST per checked row's data-id.
  document.querySelectorAll('table[data-bulk-target]').forEach(function (table) {
    const target = table.getAttribute('data-bulk-target');
    if (!target) return;
    const headerCb = table.querySelector('thead .row-check');
    const rowCbs = function () { return Array.from(table.querySelectorAll('tbody .row-check')); };
    const bar = ensureBulkBar(table);
    function refresh() {
      const checked = rowCbs().filter(function (c) { return c.checked; });
      bar.querySelector('.count').textContent = checked.length + ' selected';
      bar.classList.toggle('on', checked.length > 0);
    }
    if (headerCb) {
      headerCb.addEventListener('change', function () {
        rowCbs().forEach(function (c) { c.checked = headerCb.checked; });
        refresh();
      });
    }
    table.addEventListener('change', function (e) {
      if (e.target && e.target.classList.contains('row-check')) refresh();
    });
    bar.querySelector('[data-bulk-delete]').addEventListener('click', function () {
      const ids = rowCbs().filter(function (c) { return c.checked; })
                          .map(function (c) { return c.getAttribute('data-id'); });
      if (!ids.length) return;
      if (typeof HTMLDialogElement !== 'function') {
        if (!confirm('Delete ' + ids.length + ' row(s)? This cannot be undone.')) return;
        submitBulkDelete(ids, target);
        return;
      }
      const dlg = ensureConfirmDialog();
      dlg.querySelector('[data-msg]').textContent = 'Delete ' + ids.length + ' row(s)? This cannot be undone.';
      pendingForm = null;
      // Custom OK handler for the bulk delete -- reuses the dialog
      // but bypasses the form-submit wiring.
      const ok = dlg.querySelector('[data-ok]');
      const handler = function () {
        ok.removeEventListener('click', handler);
        dlg.close();
        submitBulkDelete(ids, target);
      };
      ok.addEventListener('click', handler, { once: true });
      dlg.showModal();
    });
  });
  function ensureBulkBar(table) {
    let bar = table.closest('.tablewrap')?.previousElementSibling;
    if (bar && bar.classList.contains('bulk-bar')) return bar;
    bar = document.createElement('div');
    bar.className = 'bulk-bar';
    bar.innerHTML =
      '<span class="count">0 selected</span>' +
      '<button type="button" class="danger sm" data-bulk-delete><svg><use href="#i-trash"/></svg>Delete selected</button>';
    const wrap = table.closest('.tablewrap');
    if (wrap && wrap.parentNode) wrap.parentNode.insertBefore(bar, wrap);
    return bar;
  }
  function submitBulkDelete(ids, target) {
    let remaining = ids.length;
    let failed = 0;
    ids.forEach(function (id) {
      const url = target.replace('{id}', encodeURIComponent(id));
      fetch(url, { method: 'POST', credentials: 'same-origin' })
        .then(function (r) { if (!r.ok) failed++; })
        .catch(function () { failed++; })
        .finally(function () {
          remaining--;
          if (remaining === 0) {
            // Refresh the page to reflect the new server state.
            window.location.reload();
          }
        });
    });
  }

  // ---- Logs tail ----------------------------------------------------
  // A page that opts in puts an element with id="logs-tail" and we
  // poll /api/v1/logs?... at the configured interval, prepending new
  // events. Stops automatically when the tab is hidden.
  const tail = document.getElementById('logs-tail');
  if (tail) {
    const url = tail.getAttribute('data-url') || '/api/v1/logs?limit=50';
    let lastID = parseInt(tail.getAttribute('data-since-id') || '0', 10) || 0;
    let timer = null;
    const intervalMs = parseInt(tail.getAttribute('data-interval-ms') || '4000', 10);
    function poll() {
      fetch(url, { credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : []; })
        .then(function (events) {
          if (!Array.isArray(events)) return;
          // Server returns newest-first; iterate reversed for prepend.
          events.slice().reverse().forEach(function (ev) {
            if (!ev.id || ev.id <= lastID) return;
            lastID = ev.id;
            const row = document.createElement('div');
            row.className = 'row ' + (ev.level === 'ERROR' ? 'error' : (ev.level === 'WARN' ? 'warn' : ''));
            row.innerHTML =
              '<span class="ts">' + escapeHTML(new Date(ev.occurred_at).toLocaleTimeString()) + '</span>' +
              '<span class="lvl">' + escapeHTML(ev.level || '') + '</span>' +
              '<span class="msg"><code>' + escapeHTML(ev.component) + '</code> ' + escapeHTML(ev.action) +
              (ev.target ? ' <span class="muted">(' + escapeHTML(ev.target) + ')</span>' : '') + '</span>';
            tail.insertBefore(row, tail.firstChild);
          });
          // Cap visible rows so the page doesn't grow forever.
          while (tail.childElementCount > 200) tail.removeChild(tail.lastChild);
        })
        .catch(function () {});
    }
    function start() { if (!timer) { poll(); timer = setInterval(poll, intervalMs); } }
    function stop()  { if (timer) { clearInterval(timer); timer = null; } }
    document.addEventListener('visibilitychange', function () {
      if (document.hidden) stop(); else start();
    });
    start();
  }

  // ---- Keyboard shortcuts -------------------------------------------
  // "/" focuses the first filter input on the page; "?" shows a tiny
  // help; "g i" go to images, "g m" machines, "g l" logs (mirroring
  // common Gmail-style nav).
  let gPressed = false;
  document.addEventListener('keydown', function (e) {
    // ignore when typing in form fields
    const t = e.target;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) {
      // except for ESC, which always closes the confirm dialog
      if (e.key === 'Escape') {
        const dlg = document.getElementById('ad-confirm');
        if (dlg && dlg.open) dlg.close();
      }
      return;
    }
    if (e.key === '/') {
      const inp = document.querySelector('input[data-filter]');
      if (inp) { e.preventDefault(); inp.focus(); inp.select(); }
      return;
    }
    if (e.key === 'g') { gPressed = true; setTimeout(function () { gPressed = false; }, 1000); return; }
    if (gPressed) {
      const map = { i: '/portal/images', m: '/portal/machines', l: '/portal/logs', s: '/portal/settings', u: '/portal/unattends', d: '/portal/drivers', o: '/portal/loadouts', w: '/portal/software' };
      const dest = map[e.key];
      if (dest) { e.preventDefault(); window.location.href = dest; }
      gPressed = false;
      return;
    }
    if (e.key === '?') {
      e.preventDefault();
      const dlg = ensureHelpDialog();
      dlg.showModal();
    }
  });
  function ensureHelpDialog() {
    let dlg = document.getElementById('ad-help');
    if (dlg) return dlg;
    dlg = document.createElement('dialog');
    dlg.id = 'ad-help';
    dlg.className = 'modal';
    dlg.innerHTML =
      '<div class="body"><h3>Keyboard shortcuts</h3>' +
      '<dl class="kv" style="background:transparent;border:none;padding:0;box-shadow:none;">' +
      '<dt><kbd>/</kbd></dt><dd>focus filter box</dd>' +
      '<dt><kbd>g</kbd> <kbd>i</kbd></dt><dd>go to Images</dd>' +
      '<dt><kbd>g</kbd> <kbd>m</kbd></dt><dd>go to Machines</dd>' +
      '<dt><kbd>g</kbd> <kbd>l</kbd></dt><dd>go to Logs</dd>' +
      '<dt><kbd>g</kbd> <kbd>u</kbd></dt><dd>go to Unattends</dd>' +
      '<dt><kbd>g</kbd> <kbd>d</kbd></dt><dd>go to Drivers</dd>' +
      '<dt><kbd>g</kbd> <kbd>w</kbd></dt><dd>go to soft<u>w</u>are</dd>' +
      '<dt><kbd>g</kbd> <kbd>o</kbd></dt><dd>go to Loadouts</dd>' +
      '<dt><kbd>g</kbd> <kbd>s</kbd></dt><dd>go to Settings</dd>' +
      '<dt><kbd>?</kbd></dt><dd>this help</dd>' +
      '</dl></div>' +
      '<div class="actions"><button type="button" data-close>Close</button></div>';
    document.body.appendChild(dlg);
    dlg.querySelector('[data-close]').addEventListener('click', function () { dlg.close(); });
    return dlg;
  }

  // ---- Helpers ------------------------------------------------------
  function escapeHTML(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
  }
})();
