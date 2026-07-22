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
  // The value is mirrored into the URL hash (replaceState, so no history
  // spam), which is what lets back/forward — and a reload — restore the
  // filter until the operator clears it.
  document.querySelectorAll('input[data-filter]').forEach(function (inp) {
    const target = document.querySelector(inp.getAttribute('data-filter'));
    if (!target) return;
    const hashKey = 'f-' + (target.id || 'tbl');
    try {
      const saved = new URLSearchParams(location.hash.slice(1)).get(hashKey);
      if (saved && !inp.value) inp.value = saved;
    } catch (_) {}
    inp.addEventListener('input', function () {
      applyFilter(target, inp.value);
      writeFilterHash(hashKey, inp.value);
    });
    // Apply once in case the hash (or the markup) carried a value.
    if (inp.value) applyFilter(target, inp.value);
  });
  function writeFilterHash(key, val) {
    try {
      const p = new URLSearchParams(location.hash.slice(1));
      if (val) p.set(key, val); else p.delete(key);
      const s = p.toString();
      history.replaceState(null, '', location.pathname + location.search + (s ? '#' + s : ''));
    } catch (_) {}
  }
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

  // ---- Server-side list filter ---------------------------------------
  // <input data-server-filter name="q"> inside a GET form. The list is
  // filtered and paginated server-side, so the filter must live in the
  // URL — that's also what makes browser back/forward keep it until the
  // operator clears it. Typing navigates after a short debounce using
  // location.replace (no history spam from intermediate keystrokes);
  // Enter navigates immediately with a real history entry; Escape (or
  // the clear button) clears. Focus is restored after each navigation
  // so typing flows through the reloads.
  (function initServerFilter() {
    var inp = document.querySelector('input[data-server-filter]');
    if (!inp) return;
    var FOCUS_KEY = 'ad-filter-focus';
    try {
      if (sessionStorage.getItem(FOCUS_KEY) === location.pathname) {
        sessionStorage.removeItem(FOCUS_KEY);
        inp.focus();
        var n = inp.value.length;
        if (inp.setSelectionRange) inp.setSelectionRange(n, n);
      }
    } catch (_) {}
    function urlFor(value) {
      var u = new URL(location.href);
      if (value) u.searchParams.set('q', value); else u.searchParams.delete('q');
      u.searchParams.delete('page'); // a changed filter restarts at page 1
      return u.pathname + u.search;
    }
    function go(value, push) {
      var dest = urlFor(value);
      if (dest === location.pathname + location.search) return;
      try { sessionStorage.setItem(FOCUS_KEY, location.pathname); } catch (_) {}
      if (push) location.assign(dest); else location.replace(dest);
    }
    var timer = null;
    inp.addEventListener('input', function (e) {
      clearTimeout(timer);
      // Synthetic input events come from the clear button — apply now.
      if (!e.isTrusted) { go(inp.value, false); return; }
      timer = setTimeout(function () { go(inp.value, false); }, 450);
    });
    inp.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') {
        e.preventDefault();
        clearTimeout(timer);
        go(inp.value, true);
      } else if (e.key === 'Escape' && inp.value) {
        e.preventDefault();
        clearTimeout(timer);
        inp.value = '';
        go('', false);
      }
    });
    // No-JS fallback is the form's own GET submit; don't double-navigate.
    if (inp.form) inp.form.addEventListener('submit', function () { clearTimeout(timer); });
  })();

  // ---- Items-per-page selector ----------------------------------------
  // <select data-page-size> navigates with ?size=N, returning to page 1.
  // The server remembers the choice in a cookie so it sticks.
  document.querySelectorAll('select[data-page-size]').forEach(function (sel) {
    sel.addEventListener('change', function () {
      var u = new URL(location.href);
      u.searchParams.set('size', sel.value);
      u.searchParams.delete('page');
      location.assign(u.pathname + u.search);
    });
  });

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
    // Format a log timestamp as date+time in the operator's configured zone
    // (body[data-tz]) so the live tail matches the server-rendered table
    // ("11 Jun 2026, 14:47:21"), not the browser's local clock.
    const tailTZ = document.body.getAttribute('data-tz') || 'UTC';
    function tailTs(iso) {
      try {
        return new Date(iso).toLocaleString('en-GB', {
          timeZone: tailTZ, day: 'numeric', month: 'short', year: 'numeric',
          hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
        });
      } catch (e) { return new Date(iso).toLocaleString(); }
    }
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
            const machine = ev.machine_name || ev.actor || '';
            row.innerHTML =
              '<span class="ts">' + escapeHTML(tailTs(ev.occurred_at)) + '</span>' +
              '<span class="lvl">' + escapeHTML(ev.level || '') + '</span>' +
              '<span class="mc">' + (machine ? escapeHTML(machine) : '<span class="muted">—</span>') + '</span>' +
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

  // ---- Logs table: expand rows, level pills, text filter, presets ----
  // The logs table renders each event as a summary row plus a hidden detail
  // row holding the Fields JSON. Clicking (or Enter/Space on) a summary row
  // toggles its detail, which is pretty-printed on first open. Level pills
  // and the text box filter the summary rows (and collapse hidden ones);
  // the "Last N" buttons set the Since field and re-run the server search.
  (function initLogsTable() {
    const table = document.getElementById('log-rows');
    if (!table) return;
    const rows = Array.prototype.slice.call(table.querySelectorAll('tr.log-row'));

    function detailOf(row) {
      const d = row.nextElementSibling;
      return d && d.classList.contains('log-detail') ? d : null;
    }
    function setOpen(row, open) {
      const d = detailOf(row);
      row.classList.toggle('open', open);
      row.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (!d) return;
      if (open) {
        const pre = d.querySelector('.log-fields');
        if (pre && !pre.dataset.pretty) {
          pre.dataset.pretty = '1';
          try { pre.textContent = JSON.stringify(JSON.parse(pre.textContent), null, 2); } catch (_) {}
        }
        d.hidden = false;
      } else {
        d.hidden = true;
      }
    }
    table.addEventListener('click', function (e) {
      const row = e.target.closest('tr.log-row');
      if (!row) return;
      setOpen(row, row.getAttribute('aria-expanded') !== 'true');
    });
    table.addEventListener('keydown', function (e) {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      const row = e.target.closest('tr.log-row');
      if (!row) return;
      e.preventDefault();
      setOpen(row, row.getAttribute('aria-expanded') !== 'true');
    });

    let activeLevel = 'all';
    let query = '';
    function apply() {
      rows.forEach(function (row) {
        const lvlOK = activeLevel === 'all' || row.getAttribute('data-level') === activeLevel;
        const txtOK = !query || (row.innerText || '').toLowerCase().indexOf(query) !== -1;
        const ok = lvlOK && txtOK;
        row.style.display = ok ? '' : 'none';
        const d = detailOf(row);
        if (!ok) { setOpen(row, false); if (d) d.style.display = 'none'; }
        else if (d) { d.style.display = ''; }
      });
    }
    const pills = document.querySelectorAll('[data-level-pill]');
    pills.forEach(function (p) {
      p.addEventListener('click', function () {
        activeLevel = p.getAttribute('data-level-pill');
        pills.forEach(function (q) { q.classList.toggle('on', q === p); });
        apply();
      });
    });
    const filterInp = document.getElementById('log-filter');
    if (filterInp) {
      filterInp.addEventListener('input', function () {
        query = filterInp.value.trim().toLowerCase();
        apply();
      });
    }

    // Time-range presets: compute (now - minutes) and submit the search form.
    // Formatted in UTC because the server parses the Since field as UTC, so
    // this makes "Last 1h" filter the actual last hour regardless of zone.
    document.querySelectorAll('[data-since-preset]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        const since = document.getElementById('log-since');
        const form = document.getElementById('log-search');
        if (!since || !form) return;
        const mins = parseInt(btn.getAttribute('data-since-preset'), 10) || 0;
        const d = new Date(Date.now() - mins * 60000);
        const pad = function (n) { return String(n).padStart(2, '0'); };
        since.value = d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1) + '-' + pad(d.getUTCDate()) +
          'T' + pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes());
        form.submit();
      });
    });
  })();

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
      const inp = document.querySelector('input[data-filter], input[data-server-filter]');
      if (inp) { e.preventDefault(); inp.focus(); inp.select(); }
      return;
    }
    if (e.key === 'g') { gPressed = true; setTimeout(function () { gPressed = false; }, 1000); return; }
    if (gPressed) {
      const map = { i: '/portal/images', m: '/portal/machines', l: '/portal/logs', s: '/portal/settings', u: '/portal/unattends', d: '/portal/drivers', o: '/portal/loadouts', w: '/portal/software', n: '/portal/notifications', b: '/portal/bulk' };
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
      '<dt><kbd>g</kbd> <kbd>n</kbd></dt><dd>go to Notifications</dd>' +
      '<dt><kbd>g</kbd> <kbd>b</kbd></dt><dd>go to Bulk operations</dd>' +
      '<dt><kbd>g</kbd> <kbd>s</kbd></dt><dd>go to Settings</dd>' +
      '<dt><kbd>?</kbd></dt><dd>this help</dd>' +
      '</dl></div>' +
      '<div class="actions"><button type="button" data-close>Close</button></div>';
    document.body.appendChild(dlg);
    dlg.querySelector('[data-close]').addEventListener('click', function () { dlg.close(); });
    return dlg;
  }

  // ---- Upload progress ----------------------------------------------
  // Opt-in: <form data-upload-progress ...> with a <input type=file>
  // inside. We hijack submit, send via XHR (NOT fetch, since fetch
  // doesn't expose upload progress events), update an inline
  // <progress> bar + label, and on 2xx follow the redirect / reload.
  // Big payloads (multi-GB ISOs) are the main use case so the bar's
  // value is real-time, not a fake animation.
  document.addEventListener('submit', function (e) {
    const form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (!form.hasAttribute('data-upload-progress')) return;
    const fileInput = form.querySelector('input[type=file]');
    if (!fileInput || !fileInput.files || fileInput.files.length === 0) {
      // Let the browser handle the "required" validation message.
      return;
    }
    e.preventDefault();
    startUpload(form);
  });

  function startUpload(form) {
    const fileInput = form.querySelector('input[type=file]');
    const file = fileInput.files[0];
    const ui = ensureProgressUI(form);
    ui.label.textContent = file.name + ' — preparing…';
    ui.bar.value = 0;
    ui.bar.max = 100;
    ui.wrap.hidden = false;
    ui.error.hidden = true;
    setSubmitDisabled(form, true);

    const fd = new FormData(form);
    const xhr = new XMLHttpRequest();
    xhr.open(form.method || 'POST', form.action, true);
    // Tell the server we want JSON if it has a JSON path; harmless
    // when the response is HTML.
    xhr.setRequestHeader('Accept', 'text/html, application/json');

    const startedAt = Date.now();
    xhr.upload.addEventListener('progress', function (evt) {
      if (!evt.lengthComputable) {
        ui.label.textContent = file.name + ' — uploading ' + formatBytes(evt.loaded) + '…';
        return;
      }
      const pct = (evt.loaded / evt.total) * 100;
      ui.bar.value = pct;
      const secs = Math.max(0.001, (Date.now() - startedAt) / 1000);
      const rate = evt.loaded / secs; // bytes/sec
      const eta = (evt.total - evt.loaded) / Math.max(rate, 1);
      ui.label.textContent =
        file.name + ' — ' +
        formatBytes(evt.loaded) + ' / ' + formatBytes(evt.total) +
        ' (' + pct.toFixed(1) + '%, ' + formatBytes(rate) + '/s, ' +
        formatDuration(eta) + ' left)';
    });

    xhr.upload.addEventListener('load', function () {
      ui.label.textContent = file.name + ' — finalising on server…';
    });

    xhr.addEventListener('load', function () {
      setSubmitDisabled(form, false);
      if (xhr.status >= 200 && xhr.status < 400) {
        // Successful upload. The handler usually returns a redirect
        // to the edit page; XHR followed it transparently and we
        // landed on the final URL via xhr.responseURL. Reload to
        // that URL so the operator sees the post-upload state.
        ui.bar.value = 100;
        ui.label.textContent = file.name + ' — done. Reloading…';
        const dst = xhr.responseURL || window.location.href;
        window.location.assign(dst);
        return;
      }
      ui.error.hidden = false;
      ui.error.textContent =
        'Upload failed: HTTP ' + xhr.status + (xhr.statusText ? ' ' + xhr.statusText : '') +
        (xhr.responseText ? ' — ' + truncate(xhr.responseText, 240) : '');
    });

    xhr.addEventListener('error', function () {
      setSubmitDisabled(form, false);
      ui.error.hidden = false;
      ui.error.textContent = 'Upload failed: network error. The server may have closed the connection -- check journalctl on the server.';
    });

    xhr.addEventListener('abort', function () {
      setSubmitDisabled(form, false);
      ui.error.hidden = false;
      ui.error.textContent = 'Upload aborted.';
    });

    xhr.send(fd);
    ui.abortBtn.onclick = function () { xhr.abort(); };
  }

  // startFolderUpload uploads every file the operator picked with a
  // webkitdirectory input, preserving the folder structure. It sends
  // ONE request per file (each a "relpath" field + the "file" part) so
  // a multi-GB directory tree — e.g. the MS Office offline install
  // files — never becomes a single giant request that exhausts browser
  // memory or trips a proxy's upload-size limit. The server pairs the
  // relpath with the file and recreates the sub-directory under the
  // package's files/ tree. Failures are per-file (partial success is
  // kept), and a run of consecutive failures stops early since that is
  // the signature of the server being out of disk space or behind an
  // upload-size limit.
  function startFolderUpload(form, input) {
    const files = Array.prototype.slice.call(input.files);
    if (!files.length) return;
    const ui = ensureProgressUI(form);
    const total = files.length;
    let totalBytes = 0;
    files.forEach(function (f) { totalBytes += f.size; });

    ui.bar.value = 0;
    ui.bar.max = 100;
    ui.wrap.hidden = false;
    ui.error.hidden = true;
    ui.label.textContent = 'Uploading 1 of ' + total + '…';
    setSubmitDisabled(form, true);

    // Stop early after this many consecutive failures — a full disk or a
    // proxy body-size limit fails every file, and hammering the server
    // hundreds of times helps nobody.
    const consecutiveFailLimit = 3;

    let index = 0;          // next file to send
    let doneBytes = 0;      // bytes accounted for (uploaded or skipped-on-failure)
    let failed = 0;
    let consecutiveFails = 0;
    const errors = [];      // { name, msg }
    let currentXhr = null;
    let aborted = false;
    const startedAt = Date.now();

    ui.abortBtn.onclick = function () {
      aborted = true;
      if (currentXhr) currentXhr.abort();
    };

    function summarise() {
      const shown = errors.slice(0, 3).map(function (e) {
        return e.name + ' (' + e.msg + ')';
      }).join('; ');
      return shown + (errors.length > 3 ? ' …' : '');
    }

    function done() {
      setSubmitDisabled(form, false);
      if (failed === 0) {
        ui.bar.value = 100;
        ui.label.textContent = total + ' file' + (total === 1 ? '' : 's') + ' uploaded — reloading…';
        window.location.reload();
        return;
      }
      // Some failed: reload so the files that DID upload appear, then
      // surface the failure summary. Reload wins the race visually, so
      // set the flash-style error before reloading isn't useful; instead
      // leave the summary in place and let the operator reload manually
      // if they want the partial list — but a repeated-failure stop
      // almost always means nothing got through, so we keep the page.
      ui.error.hidden = false;
      ui.error.textContent =
        failed + ' of ' + total + ' file(s) failed: ' + summarise() +
        '. Likely causes: the server is out of disk space or behind an ' +
        'upload-size limit (check journalctl and df on the server). ' +
        'Files that succeeded are saved — reload to see them.';
    }

    function afterFailure(name, msg) {
      failed++;
      consecutiveFails++;
      errors.push({ name: name, msg: msg });
      if (consecutiveFails >= consecutiveFailLimit && index + 1 < total) {
        setSubmitDisabled(form, false);
        ui.error.hidden = false;
        ui.error.textContent =
          'Stopped after ' + consecutiveFails + ' consecutive failures (' + msg + '). ' +
          'The server is most likely out of disk space or behind an upload-size ' +
          'limit — check journalctl and df on the server, then try again.';
        return true; // halt
      }
      return false;
    }

    function uploadNext() {
      if (aborted) {
        setSubmitDisabled(form, false);
        ui.error.hidden = false;
        ui.error.textContent = 'Upload aborted after ' + index + ' of ' + total + ' file(s).';
        return;
      }
      if (index >= files.length) { done(); return; }

      const f = files[index];
      const rel = f.webkitRelativePath || f.name;
      const fd = new FormData();
      fd.append('relpath', rel);
      fd.append('file', f, f.name);

      const xhr = new XMLHttpRequest();
      currentXhr = xhr;
      xhr.open('POST', form.action, true);
      xhr.setRequestHeader('Accept', 'text/html, application/json');

      xhr.upload.addEventListener('progress', function (evt) {
        const loaded = doneBytes + (evt.lengthComputable ? evt.loaded : 0);
        if (totalBytes > 0) {
          ui.bar.value = Math.min(100, (loaded / totalBytes) * 100);
        }
        const secs = Math.max(0.001, (Date.now() - startedAt) / 1000);
        const rate = loaded / secs;
        const eta = totalBytes > 0 ? (totalBytes - loaded) / Math.max(rate, 1) : 0;
        ui.label.textContent =
          'Uploading ' + (index + 1) + ' of ' + total + ' — ' + rel + ' — ' +
          formatBytes(loaded) + ' / ' + formatBytes(totalBytes) +
          ' (' + formatBytes(rate) + '/s, ' + formatDuration(eta) + ' left)';
      });

      xhr.addEventListener('load', function () {
        currentXhr = null;
        doneBytes += f.size;
        if (xhr.status >= 200 && xhr.status < 400) {
          consecutiveFails = 0;
          index++;
          uploadNext();
          return;
        }
        const msg = 'HTTP ' + xhr.status;
        if (afterFailure(rel, msg)) return;
        index++;
        uploadNext();
      });

      xhr.addEventListener('error', function () {
        currentXhr = null;
        doneBytes += f.size;
        if (afterFailure(rel, 'network error')) return;
        index++;
        uploadNext();
      });

      xhr.addEventListener('abort', function () {
        currentXhr = null;
        if (aborted) { uploadNext(); return; } // aborted branch reports it
        doneBytes += f.size;
        if (afterFailure(rel, 'aborted')) return;
        index++;
        uploadNext();
      });

      xhr.send(fd);
    }

    uploadNext();
  }

  // ensureProgressUI returns the progress-bar widget for a form,
  // creating it on first call. The widget is appended at the end of
  // the form so the markup stays simple in the templates: just slap
  // data-upload-progress on the form and we do the rest.
  function ensureProgressUI(form) {
    let wrap = form.querySelector('.upload-progress');
    if (wrap) {
      return {
        wrap,
        bar: wrap.querySelector('progress'),
        label: wrap.querySelector('.upload-progress-label'),
        error: wrap.querySelector('.upload-progress-error'),
        abortBtn: wrap.querySelector('.upload-progress-abort'),
      };
    }
    wrap = document.createElement('div');
    wrap.className = 'upload-progress';
    wrap.hidden = true;
    wrap.innerHTML =
      '<div class="upload-progress-row">' +
        '<progress class="upload-progress-bar" value="0" max="100"></progress>' +
        '<button type="button" class="btn ghost sm upload-progress-abort">Cancel</button>' +
      '</div>' +
      '<div class="upload-progress-label small muted"></div>' +
      '<div class="upload-progress-error small warn" hidden></div>';
    form.appendChild(wrap);
    return {
      wrap,
      bar: wrap.querySelector('progress'),
      label: wrap.querySelector('.upload-progress-label'),
      error: wrap.querySelector('.upload-progress-error'),
      abortBtn: wrap.querySelector('.upload-progress-abort'),
    };
  }

  function setSubmitDisabled(form, disabled) {
    form.querySelectorAll('button[type=submit], input[type=submit]').forEach(function (b) {
      b.disabled = disabled;
    });
  }

  function formatBytes(n) {
    if (n < 1024) return n.toFixed(0) + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KiB';
    if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + ' MiB';
    return (n / (1024 * 1024 * 1024)).toFixed(2) + ' GiB';
  }

  function formatDuration(secs) {
    if (!isFinite(secs) || secs < 0) return '?';
    if (secs < 60) return secs.toFixed(0) + 's';
    if (secs < 3600) {
      const m = Math.floor(secs / 60);
      const s = Math.floor(secs % 60);
      return m + 'm ' + s + 's';
    }
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return h + 'h ' + m + 'm';
  }

  function truncate(s, n) {
    s = String(s).replace(/\s+/g, ' ').trim();
    return s.length > n ? s.slice(0, n - 1) + '…' : s;
  }

  // ---- ISO boot-media prepare progress ------------------------------
  // On the ISO edit page a background extract+split may be running. Poll
  // prep-status, drive the .prep-progress bar, and reload the page once
  // it finishes so the Boot media panel shows the result. We only reload
  // if we actually observed an active phase, so a page that loads after
  // the job already finished doesn't loop.
  (function initPrepProgress() {
    const box = document.querySelector('.prep-progress[data-iso-id]');
    if (!box) return;
    const id = box.getAttribute('data-iso-id');
    const bar = box.querySelector('.prep-bar');
    const phaseEl = box.querySelector('.prep-phase');
    const pctEl = box.querySelector('.prep-pct');
    let sawActive = false;

    function schedule() { setTimeout(tick, 1500); }

    function tick() {
      fetch('/portal/isos/' + id + '/prep-status', { headers: { Accept: 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s) { schedule(); return; }
          const active = s.phase === 'extracting' || s.phase === 'splitting';
          if (active) {
            sawActive = true;
            box.hidden = false;
            if (phaseEl) phaseEl.textContent = s.phase === 'splitting' ? 'Splitting install image…' : 'Extracting ISO…';
            if (bar) bar.value = s.percent || 0;
            if (pctEl) {
              pctEl.textContent = (s.percent || 0) + '%' +
                (s.total_bytes ? ' of ' + formatBytes(s.total_bytes) : '');
            }
            schedule();
          } else if (s.finished && sawActive) {
            window.location.reload();
          }
        })
        .catch(function () { schedule(); });
    }
    tick();
  })();

  // ---- Machine deploy progress --------------------------------------
  // On the machine detail page a deployment may be in flight (imaging or
  // installing software). Poll deploy-status, drive the .deploy-bar, and
  // reload the page once it finishes so the at-a-glance summary settles.
  // We only reload if we actually observed an active deploy, so a page
  // loaded after completion doesn't loop.
  (function initDeployProgress() {
    const box = document.querySelector('.deploy-progress[data-machine-id]');
    if (!box) return;
    const id = box.getAttribute('data-machine-id');
    const bar = box.querySelector('.deploy-bar');
    const phaseEl = box.querySelector('.deploy-phase');
    const pctEl = box.querySelector('.deploy-pct');
    let sawActive = false;

    function schedule() { setTimeout(tick, 1500); }

    function tick() {
      fetch('/portal/machines/' + id + '/deploy-status', { headers: { Accept: 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s) { schedule(); return; }
          if (s.active) {
            sawActive = true;
            box.hidden = false;
            box.classList.toggle('stalled', !!s.stalled);
            if (phaseEl) phaseEl.textContent = s.stalled ? 'Stalled — no recent check-in' : (s.label || 'In progress');
            if (bar) {
              if (s.indeterminate) { bar.removeAttribute('value'); }
              else { bar.value = s.percent || 0; }
            }
            if (pctEl) pctEl.textContent = s.indeterminate ? '' : ((s.percent || 0) + '%');
            schedule();
          } else if (s.finished && sawActive) {
            window.location.reload();
          }
          // Not active and never saw active: nothing in flight; stop polling.
        })
        .catch(function () { schedule(); });
    }
    tick();
  })();

  // ---- Action / schedule picker -------------------------------------
  // A <fieldset class="action-picker"> of radio tiles, immediately followed
  // by a <div class="action-panels"> whose [data-action-panel="value"] blocks
  // are shown/hidden to match the checked radio. Replaces the old per-page
  // <select onchange> show/hide scripts; works for the action picker, the
  // schedule picker, and any future tile group.
  document.querySelectorAll('.action-picker').forEach(function (fs) {
    var panels = fs.nextElementSibling;
    if (!panels || !panels.classList.contains('action-panels')) return;
    function sync() {
      var sel = fs.querySelector('input[type=radio]:checked');
      var v = sel ? sel.value : '';
      panels.querySelectorAll('[data-action-panel]').forEach(function (p) {
        p.hidden = p.getAttribute('data-action-panel') !== v;
      });
      fs.querySelectorAll('.action-tile').forEach(function (t) {
        var r = t.querySelector('input[type=radio]');
        t.classList.toggle('selected', !!(r && r.checked));
      });
    }
    fs.addEventListener('change', sync);
    sync();
  });

  // ---- Bulk operation progress --------------------------------------
  // The bulk detail page has <div data-bulk-progress data-op-id>. Poll the
  // operation's progress endpoint, drive the rollup bar/counts, and reload
  // once it reaches a terminal state so the settled view (final job rows)
  // appears.
  (function initBulkProgress() {
    var box = document.querySelector('[data-bulk-progress][data-op-id]');
    if (!box) return;
    var id = box.getAttribute('data-op-id');
    var bar = box.querySelector('[data-prog-bar]');
    var counts = box.querySelector('[data-prog-counts]');
    var pct = box.querySelector('[data-prog-pct]');
    function schedule() { setTimeout(tick, 2500); }
    function tick() {
      fetch('/portal/bulk/' + id + '/progress', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s) { schedule(); return; }
          if (bar) { bar.value = (s.ok + s.failed + s.cancelled) || 0; bar.max = s.total || 1; }
          if (counts) {
            counts.textContent = s.ok + ' ok · ' + s.failed + ' failed · ' + s.running +
              ' running · ' + s.queued + ' queued' + (s.cancelled ? ' · ' + s.cancelled + ' cancelled' : '');
          }
          if (pct) pct.textContent = (s.percent || 0) + '%';
          // Keep polling while work is outstanding; reload once it settles so
          // the status badge and per-job results refresh from the server.
          if (s.running > 0 || s.queued > 0) { schedule(); }
          else if (s.finished) { /* terminal — leave as is */ }
        })
        .catch(function () { schedule(); });
    }
    schedule();
  })();

  // ---- Notification badge poll ---------------------------------------
  (function initNotifyBadge() {
    var badge = document.getElementById('notify-badge');
    if (!badge) return;
    function poll() {
      fetch('/api/v1/notifications/unread-count', {
        credentials: 'same-origin',
        headers: { 'X-Requested-With': 'fetch' },
      })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) {
          if (!d) return;
          if (d.count > 0) {
            badge.textContent = d.count > 99 ? '99+' : d.count;
            badge.style.display = '';
          } else {
            badge.style.display = 'none';
          }
        })
        .catch(function () {});
    }
    poll();
    setInterval(poll, 30000);
  })();

  // ---- Drop-zone auto-upload -----------------------------------------
  document.querySelectorAll('[data-dropzone]').forEach(function (form) {
    var fileInput = form.querySelector('input[type=file]');
    if (!fileInput) return;

    ['dragenter', 'dragover'].forEach(function (evt) {
      form.addEventListener(evt, function (e) {
        e.preventDefault();
        form.classList.add('dragover');
      });
    });
    ['dragleave', 'drop'].forEach(function (evt) {
      form.addEventListener(evt, function (e) {
        e.preventDefault();
        form.classList.remove('dragover');
      });
    });
    form.addEventListener('drop', function (e) {
      if (e.dataTransfer.files.length) {
        var accept = fileInput.getAttribute('accept');
        if (accept) {
          var exts = accept.split(',').map(function (s) { return s.trim().toLowerCase(); });
          var name = e.dataTransfer.files[0].name.toLowerCase();
          var ok = exts.some(function (ext) { return name.endsWith(ext); });
          if (!ok) {
            var ui = ensureProgressUI(form);
            ui.wrap.hidden = false;
            ui.error.hidden = false;
            ui.error.textContent = 'Wrong file type. Accepted: ' + accept;
            return;
          }
        }
        fileInput.files = e.dataTransfer.files;
        startUpload(form);
      }
    });
    fileInput.addEventListener('change', function () {
      if (fileInput.files.length) startUpload(form);
    });
  });

  // ---- Folder auto-upload (webkitdirectory, preserves structure) ------
  document.querySelectorAll('[data-folder-upload]').forEach(function (form) {
    var folderInput = form.querySelector('input[type=file]');
    if (!folderInput) return;
    folderInput.addEventListener('change', function () {
      if (folderInput.files.length) startFolderUpload(form, folderInput);
    });
  });

  // ---- Unattend TOC scroll spy ----------------------------------------
  (function () {
    var toc = document.querySelector('.unattend-toc');
    if (!toc) return;
    var links = Array.prototype.slice.call(toc.querySelectorAll('a[href^="#sec-"]'));
    if (!links.length) return;
    var sections = links.map(function (a) {
      return document.getElementById(a.getAttribute('href').slice(1));
    }).filter(Boolean);
    var active = null;
    function onScroll() {
      var y = window.scrollY + 80;
      var cur = null;
      for (var i = sections.length - 1; i >= 0; i--) {
        if (sections[i].offsetTop <= y) { cur = links[i]; break; }
      }
      if (cur === active) return;
      if (active) active.classList.remove('active');
      if (cur) cur.classList.add('active');
      active = cur;
    }
    window.addEventListener('scroll', onScroll, { passive: true });
    onScroll();
  })();

  // ---- Filter clear button -------------------------------------------
  // Works for both shapes in the templates: a bare <input class="filter">
  // (wrapped so the × can be positioned) and the <div class="filter">
  // search box (already position:relative). Clearing dispatches a
  // synthetic input event, which the client-side filter applies
  // instantly and the server-side filter treats as "navigate now".
  document.querySelectorAll('.filter').forEach(function (el) {
    var input, host;
    if (el.tagName === 'INPUT') {
      input = el;
      host = document.createElement('span');
      host.style.cssText = 'position:relative;display:inline-block';
      el.parentNode.insertBefore(host, el);
      host.appendChild(el);
    } else {
      input = el.querySelector('input');
      host = el;
    }
    if (!input) return;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'filter-clear';
    btn.setAttribute('aria-label', 'Clear filter');
    btn.textContent = '×';
    btn.style.cssText = 'position:absolute;right:6px;top:50%;transform:translateY(-50%);background:none;border:none;font-size:1.1rem;color:var(--fg-muted);cursor:pointer;padding:0 4px;display:none';
    host.appendChild(btn);
    function toggle() { btn.style.display = input.value ? '' : 'none'; }
    input.addEventListener('input', toggle);
    toggle();
    btn.addEventListener('click', function () {
      input.value = '';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      toggle();
    });
  });

  // ---- Helpers ------------------------------------------------------
  function escapeHTML(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
  }
})();
