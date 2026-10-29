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

  // ---- List search ----------------------------------------------------
  // <form data-search-form> is a plain GET form (input name="q" + Search
  // button + Clear link) and the server does the filtering. Nothing runs
  // while the operator types: the search happens on Enter or the button,
  // as a normal navigation, so every search is its own history entry and
  // Back/Forward step through them. The box is rendered from ?q=, so it
  // always matches the results. The only JS here keeps the URL tidy and
  // upgrades old bookmarks.
  document.querySelectorAll('form[data-search-form]').forEach(function (form) {
    form.addEventListener('submit', function (e) {
      if (e.defaultPrevented) return;
      // Drop empty fields (an empty search is "show everything"), so a
      // cleared box lands on the plain list URL rather than "?q=".
      e.preventDefault();
      var u = new URL(form.getAttribute('action') || location.pathname, location.href);
      new FormData(form).forEach(function (v, k) {
        if (typeof v === 'string' && v.trim() !== '') u.searchParams.set(k, k === 'q' ? v.trim() : v);
      });
      location.assign(u.pathname + u.search);
    });
  });
  // Old bookmarks carried the client-side filter in the hash
  // (#f-tbl=foo). Turn that into the server-side ?q= once, replacing the
  // entry so Back doesn't bounce through the old URL.
  (function upgradeFilterHash() {
    var form = document.querySelector('form[data-search-form]');
    if (!form || !location.hash || location.hash.indexOf('f-') === -1) return;
    var saved = '';
    try {
      new URLSearchParams(location.hash.slice(1)).forEach(function (v, k) {
        if (!saved && k.indexOf('f-') === 0 && v.trim()) saved = v.trim();
      });
    } catch (_) { return; }
    var u = new URL(location.href);
    u.hash = '';
    if (!saved || u.searchParams.get('q')) {
      history.replaceState(history.state, '', u.pathname + u.search);
      return;
    }
    u.searchParams.set('q', saved);
    u.searchParams.delete('page');
    location.replace(u.pathname + u.search);
  })();

  // ---- Unsaved-changes guard -------------------------------------------
  // <form data-dirty-guard> (the create/edit and settings forms): once the
  // operator has edited a field, leaving the page — a link, Back, closing
  // the tab — asks first. Only real user input counts (isTrusted events),
  // so a page script filling or rearranging fields doesn't arm it, and a
  // field changed back to its original value doesn't either. Submitting
  // the form disarms it. A form re-rendered after a rejected save holds
  // unsaved values by definition, so it starts armed.
  (function initDirtyGuard() {
    var forms = Array.prototype.slice.call(document.querySelectorAll('form[data-dirty-guard]'));
    if (!forms.length) return;
    function snapshot(form) {
      var parts = [];
      try {
        new FormData(form).forEach(function (v, k) {
          parts.push(k + '=' + (typeof v === 'string' ? v : (v && v.name) || ''));
        });
      } catch (_) {}
      return parts.join('&');
    }
    var state = new Map(); // form -> { base, touched, armed }
    forms.forEach(function (form) {
      var st = { base: snapshot(form), touched: false, armed: !!form.querySelector('[data-form-error]') };
      state.set(form, st);
      var mark = function (e) { if (e.isTrusted) st.touched = true; };
      form.addEventListener('input', mark, true);
      form.addEventListener('change', mark, true);
    });
    // Page scripts may still be populating fields until load; re-take the
    // baseline for forms the operator hasn't touched yet.
    window.addEventListener('load', function () {
      state.forEach(function (st, form) { if (!st.touched) st.base = snapshot(form); });
    });
    function disarm(form) {
      var st = state.get(form);
      if (st) { st.touched = false; st.armed = false; st.base = snapshot(form); }
    }
    // Bubble listener on window: runs after the form's own onsubmit and
    // the confirm-dialog handler, so a cancelled submit keeps the guard.
    window.addEventListener('submit', function (e) {
      if (!e.defaultPrevented && e.target instanceof HTMLFormElement) disarm(e.target);
    });
    // form.submit() fires no submit event (the confirm dialog uses it).
    var nativeSubmit = HTMLFormElement.prototype.submit;
    HTMLFormElement.prototype.submit = function () {
      disarm(this);
      return nativeSubmit.call(this);
    };
    function isDirty() {
      var dirty = false;
      state.forEach(function (st, form) {
        if (st.armed || (st.touched && snapshot(form) !== st.base)) dirty = true;
      });
      return dirty;
    }
    window.addEventListener('beforeunload', function (e) {
      if (!isDirty()) return;
      e.preventDefault();
      e.returnValue = ''; // the browser shows its own "Leave site?" text
      return '';
    });
  })();

  // A form re-rendered after a rejected save: put the cursor in the first
  // field with an error (or bring the form's error message into view).
  (function focusFormError() {
    var bad = document.querySelector('form [aria-invalid="true"]');
    if (bad) { bad.focus(); return; }
    var msg = document.querySelector('[data-form-error]');
    if (msg && msg.scrollIntoView) msg.scrollIntoView({ block: 'center' });
  })();

  // ---- Session expiry warning --------------------------------------------
  // The server slides the session's idle expiry on each page view or form
  // post and puts it in <meta name="ad-session-expires"> (Unix ms, with
  // the server's clock in data-now so a skewed client clock doesn't
  // matter). A few minutes before it lapses a non-blocking banner offers
  // "Stay signed in", which POSTs the keep-alive endpoint and re-arms.
  (function sessionWarning() {
    var meta = document.querySelector('meta[name="ad-session-expires"]');
    if (!meta) return;
    var expiresAt = parseInt(meta.getAttribute('content'), 10);
    var serverNow = parseInt(meta.getAttribute('data-now'), 10);
    if (!expiresAt) return;
    var skew = serverNow ? Date.now() - serverNow : 0; // client minus server
    var WARN_MS = 5 * 60 * 1000;
    var bar = null, tick = null, timer = null;
    function remaining() { return expiresAt + skew - Date.now(); }
    function fmt(ms) {
      var s = Math.max(0, Math.ceil(ms / 1000));
      return Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2);
    }
    function ensureBar() {
      if (bar) return bar;
      bar = document.createElement('div');
      bar.className = 'session-bar';
      bar.setAttribute('role', 'status');
      bar.setAttribute('aria-live', 'polite');
      bar.innerHTML = '<svg aria-hidden="true"><use href="#i-clock"/></svg>' +
        '<span data-session-msg></span>' +
        '<button type="button" class="sm" data-session-stay>Stay signed in</button>' +
        '<a class="btn secondary sm" data-session-login hidden>Sign in again</a>';
      document.body.appendChild(bar);
      bar.querySelector('[data-session-stay]').addEventListener('click', stay);
      var login = bar.querySelector('[data-session-login]');
      login.href = '/portal/login?' + new URLSearchParams({ next: location.pathname + location.search, expired: '1' }).toString();
      return bar;
    }
    function render() {
      var left = remaining();
      var b = ensureBar();
      var msg = b.querySelector('[data-session-msg]');
      if (left <= 0) {
        msg.textContent = 'Your session has expired. Anything you haven’t saved on this page will be lost if you submit it now.';
        b.querySelector('[data-session-stay]').hidden = true;
        b.querySelector('[data-session-login]').hidden = false;
        b.classList.add('expired');
        clearInterval(tick); tick = null;
        return;
      }
      msg.textContent = 'You’ll be signed out in ' + fmt(left) + ' because of inactivity.';
    }
    function show() {
      render();
      if (!tick) tick = setInterval(render, 1000);
    }
    function hide() {
      clearInterval(tick); tick = null;
      if (bar) { bar.remove(); bar = null; }
    }
    function arm() {
      clearTimeout(timer);
      var wait = remaining() - WARN_MS;
      if (wait <= 0) { show(); return; }
      // setTimeout clamps very long delays; re-check at least hourly.
      timer = setTimeout(arm, Math.min(wait, 60 * 60 * 1000));
    }
    function stay() {
      var btn = bar && bar.querySelector('[data-session-stay]');
      if (btn) btn.disabled = true;
      fetch('/portal/session/keepalive', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'X-Requested-With': 'fetch' }
      }).then(function (r) {
        if (!r.ok) throw new Error('expired');
        return r.json();
      }).then(function (body) {
        if (body && body.expires_at) {
          expiresAt = body.expires_at;
          if (body.now) skew = Date.now() - body.now;
        }
        hide();
        arm();
      }).catch(function () {
        expiresAt = Date.now() - skew - 1;
        render();
      });
    }
    arm();
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

  // ---- Logs: live tail ----------------------------------------------
  // The Logs page's tail panel polls the JSON API with the page's own
  // filters (data-url) plus after=<newest id seen>, prepending new events.
  // It runs only while the panel is open (the "Live tail" button) and the
  // tab is visible. A page that renders #logs-tail without data-manual
  // starts tailing straight away.
  const tail = document.getElementById('logs-tail');
  if (tail) {
    const baseURL = tail.getAttribute('data-url') || '/api/v1/logs?limit=50';
    let lastID = parseInt(tail.getAttribute('data-since-id') || '0', 10) || 0;
    // On a later page (or an empty one) the newest id isn't known: the first
    // poll only learns it, so the tail shows events from now on.
    let primed = lastID > 0;
    let timer = null;
    const intervalMs = parseInt(tail.getAttribute('data-interval-ms') || '4000', 10);
    const panel = document.getElementById('tail-panel');
    const btn = document.getElementById('toggle-tail');
    const tailTZ = document.body.getAttribute('data-tz') || 'UTC';
    function tailTs(iso) {
      try {
        return new Date(iso).toLocaleTimeString('en-GB', {
          timeZone: tailTZ, hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
        });
      } catch (e) { return new Date(iso).toLocaleTimeString(); }
    }
    function lvlLabel(l) { return l === 'ERROR' ? 'Error' : (l === 'WARN' ? 'Warning' : 'Info'); }
    function poll() {
      const sep = baseURL.indexOf('?') === -1 ? '?' : '&';
      const url = lastID > 0 ? baseURL + sep + 'after=' + lastID : baseURL;
      fetch(url, { credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : []; })
        .then(function (events) {
          if (!Array.isArray(events)) return;
          if (!primed) {
            events.forEach(function (ev) { if (ev.id > lastID) lastID = ev.id; });
            primed = true;
            return;
          }
          // Newest first from the server; prepend oldest first.
          events.slice().reverse().forEach(function (ev) {
            if (!ev.id || ev.id <= lastID) return;
            lastID = ev.id;
            const empty = tail.querySelector('.empty-row');
            if (empty) empty.remove();
            const row = document.createElement('div');
            row.className = 'row ' + (ev.level === 'ERROR' ? 'error' : (ev.level === 'WARN' ? 'warn' : ''));
            const machine = ev.machine_name || '';
            const mc = machine
              ? (ev.machine_id ? '<a href="/portal/machines/' + encodeURIComponent(ev.machine_id) + '">' + escapeHTML(machine) + '</a>' : escapeHTML(machine))
              : '<span class="muted">—</span>';
            row.innerHTML =
              '<span class="ts" title="' + escapeHTML(ev.occurred_at || '') + '">' + escapeHTML(tailTs(ev.occurred_at)) + '</span>' +
              '<span class="lvl">' + escapeHTML(lvlLabel(ev.level)) + '</span>' +
              '<span class="mc">' + mc + '</span>' +
              '<span class="msg">' + escapeHTML(ev.message || ev.action || '') +
              ' <code class="log-code">' + escapeHTML(ev.action || '') + '</code></span>';
            tail.insertBefore(row, tail.firstChild);
          });
          while (tail.childElementCount > 200) tail.removeChild(tail.lastChild);
        })
        .catch(function () {});
    }
    function running() { return !panel || !panel.hidden; }
    function start() { if (!timer && running() && !document.hidden) { poll(); timer = setInterval(poll, intervalMs); } }
    function stop()  { if (timer) { clearInterval(timer); timer = null; } }
    document.addEventListener('visibilitychange', function () {
      if (document.hidden) stop(); else start();
    });
    if (btn && panel) {
      btn.addEventListener('click', function () {
        panel.hidden = !panel.hidden;
        btn.classList.toggle('on', !panel.hidden);
        btn.setAttribute('aria-expanded', panel.hidden ? 'false' : 'true');
        if (panel.hidden) stop(); else start();
      });
    }
    if (!tail.hasAttribute('data-manual')) start();
  }

  // ---- Logs table: expandable rows -----------------------------------
  // Each event is a summary row plus a hidden detail row (key/value table,
  // a Raw toggle, or a folded command output). Click, Enter or Space on a
  // summary row toggles its detail. Searching is server-side (the form
  // above the table); there is no client-side filtering here.
  (function initLogsTable() {
    const table = document.getElementById('log-rows');
    if (!table) return;
    function detailOf(row) {
      const d = row.nextElementSibling;
      return d && d.classList.contains('log-detail') ? d : null;
    }
    function toggle(row) {
      const open = row.getAttribute('aria-expanded') !== 'true';
      row.classList.toggle('open', open);
      row.setAttribute('aria-expanded', open ? 'true' : 'false');
      const d = detailOf(row);
      if (d) d.hidden = !open;
    }
    table.addEventListener('click', function (e) {
      if (e.target.closest('a')) return; // links inside a row navigate
      const row = e.target.closest('tr.log-row');
      if (row) toggle(row);
    });
    table.addEventListener('keydown', function (e) {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      if (e.target.closest('a')) return;
      const row = e.target.closest('tr.log-row');
      if (!row) return;
      e.preventDefault();
      toggle(row);
    });
  })();

  // ---- Grouped primary nav --------------------------------------------
  // Each group label is a button controlling its menu (aria-expanded).
  // Click/Enter/Space toggles, ArrowDown opens and focuses the first link,
  // Up/Down move within an open menu, Esc closes and returns focus, and a
  // click elsewhere closes. Below 900px the whole nav folds behind the
  // hamburger (.nav-toggle) and every group is listed open.
  (function initNav() {
    const header = document.querySelector('header.app');
    const nav = document.getElementById('main-nav');
    if (!header || !nav) return;
    const narrow = window.matchMedia('(max-width: 900px)');
    const groups = Array.prototype.slice.call(nav.querySelectorAll('[data-nav-group]'));
    function trigger(g) { return g.querySelector('.nav-trigger'); }
    function links(g) { return Array.prototype.slice.call(g.querySelectorAll('.nav-menu a')); }
    function setOpen(g, open) {
      g.classList.toggle('open', open);
      trigger(g).setAttribute('aria-expanded', open ? 'true' : 'false');
    }
    function closeAll(except) { groups.forEach(function (g) { if (g !== except) setOpen(g, false); }); }
    groups.forEach(function (g) {
      const btn = trigger(g);
      btn.addEventListener('click', function () {
        if (narrow.matches) return; // every group is shown open on phones
        const open = !g.classList.contains('open');
        closeAll(g);
        setOpen(g, open);
      });
      btn.addEventListener('keydown', function (e) {
        if (narrow.matches || e.key !== 'ArrowDown') return;
        e.preventDefault();
        e.stopPropagation(); // the group's own Up/Down handler must not also move focus
        closeAll(g);
        setOpen(g, true);
        const first = links(g)[0];
        if (first) first.focus();
      });
      g.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && g.classList.contains('open')) {
          setOpen(g, false);
          btn.focus();
          return;
        }
        if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
        const items = links(g);
        const i = items.indexOf(document.activeElement);
        if (i < 0) return;
        e.preventDefault();
        const next = items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length];
        if (next) next.focus();
      });
      // Tabbing out of a group closes it.
      g.addEventListener('focusout', function (e) {
        if (!narrow.matches && !g.contains(e.relatedTarget)) setOpen(g, false);
      });
    });
    document.addEventListener('click', function (e) {
      if (!nav.contains(e.target)) closeAll(null);
    });
    const toggle = header.querySelector('.nav-toggle');
    if (toggle) {
      toggle.addEventListener('click', function () {
        const open = !header.classList.contains('nav-open');
        header.classList.toggle('nav-open', open);
        toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
        groups.forEach(function (g) { trigger(g).setAttribute('aria-expanded', open ? 'true' : 'false'); });
      });
      document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && header.classList.contains('nav-open')) {
          header.classList.remove('nav-open');
          toggle.setAttribute('aria-expanded', 'false');
          toggle.focus();
        }
      });
    }
    // Leaving phone width resets the per-group state.
    narrow.addEventListener('change', function () { closeAll(null); });
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
      const inp = document.querySelector('input[data-search], input[data-filter], input[data-server-filter]');
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

  // uploadItemsSequential uploads a list of { file, relpath } items ONE
  // request per file (each a "relpath" field + the "file" part). This
  // backs the merged upload area — a single file, several files, or a
  // whole directory tree (e.g. the MS Office offline install files) all
  // funnel through here, so a big tree never becomes one giant request
  // that exhausts browser memory or trips a proxy's upload-size limit.
  // The server pairs the relpath with the file and recreates the
  // sub-directory under the package's files/ tree. Failures are per-file
  // (partial success is kept), and a run of consecutive failures stops
  // early since that is the signature of the server being out of disk
  // space or behind an upload-size limit.
  function uploadItemsSequential(form, items) {
    if (!items.length) return;
    const ui = ensureProgressUI(form);
    const total = items.length;
    let totalBytes = 0;
    items.forEach(function (it) { totalBytes += it.file.size; });

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
      if (index >= items.length) { done(); return; }

      const f = items[index].file;
      const rel = items[index].relpath;
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
  // The bulk detail page has <div data-bulk-progress data-op-id> around a
  // segmented bar (see _bulk_progress.html). While jobs are outstanding,
  // poll the operation's progress endpoint and update each segment's width
  // and legend count in place; once nothing is left waiting or running,
  // reload so the status badge and per-job results come from the server.
  (function initBulkProgress() {
    var box = document.querySelector('[data-bulk-progress][data-op-id]');
    if (!box || !(+box.getAttribute('data-outstanding') > 0)) return;
    var id = box.getAttribute('data-op-id');
    var pct = box.querySelector('[data-prog-pct]');
    function schedule() { setTimeout(tick, 2500); }
    function tick() {
      fetch('/portal/bulk/' + id + '/progress', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s) { schedule(); return; }
          ['ok', 'failed', 'running', 'queued', 'cancelled'].forEach(function (k) {
            var n = s[k] || 0;
            var seg = box.querySelector('[data-seg="' + k + '"]');
            if (seg) seg.style.width = (s.total ? (n * 100 / s.total) : 0).toFixed(2) + '%';
            var c = box.querySelector('[data-seg-count="' + k + '"]');
            if (c) c.textContent = n;
            var li = box.querySelector('[data-seg-item="' + k + '"]');
            if (li) li.classList.toggle('zero', n === 0);
          });
          if (pct) pct.textContent = s.percent || 0;
          if (s.running > 0 || s.queued > 0) { schedule(); } else { location.reload(); }
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
  // Two flavours:
  //   * plain (ISO, drivers, bundle .zip …): one file via startUpload.
  //   * data-folder-capable (software payload files): a file, several
  //     files, or a whole folder tree — each file uploaded as its own
  //     request with a relative path via uploadItemsSequential. The one
  //     drop area handles both files and folders (drag or click).

  // filesToItems maps a FileList to { file, relpath } items, using each
  // file's webkitRelativePath (set for a folder pick) or bare name.
  function filesToItems(fileList) {
    return Array.prototype.slice.call(fileList).map(function (f) {
      return { file: f, relpath: f.webkitRelativePath || f.name };
    });
  }

  // walkEntry resolves a dropped file/directory entry into { file, relpath }
  // items, recursing into sub-directories so a dragged folder keeps its
  // structure. readEntries returns in batches, so we drain it fully.
  function walkEntry(entry, prefix) {
    if (entry.isFile) {
      return new Promise(function (resolve) {
        entry.file(
          function (f) { resolve([{ file: f, relpath: prefix + entry.name }]); },
          function () { resolve([]); });
      });
    }
    if (entry.isDirectory) {
      var reader = entry.createReader();
      var collected = [];
      return new Promise(function (resolve) {
        (function readBatch() {
          reader.readEntries(function (batch) {
            if (!batch.length) {
              Promise.all(collected.map(function (e) {
                return walkEntry(e, prefix + entry.name + '/');
              })).then(function (lists) {
                resolve(Array.prototype.concat.apply([], lists));
              });
              return;
            }
            collected = collected.concat(Array.prototype.slice.call(batch));
            readBatch();
          }, function () { resolve([]); });
        })();
      });
    }
    return Promise.resolve([]);
  }

  // dropToItems turns a drop's DataTransfer into { file, relpath } items,
  // recursing into dropped directories where the entries API is available,
  // else falling back to the flat file list.
  function dropToItems(dataTransfer) {
    var dtItems = dataTransfer.items;
    var entries = [];
    if (dtItems && dtItems.length) {
      for (var i = 0; i < dtItems.length; i++) {
        var getAsEntry = dtItems[i].webkitGetAsEntry || dtItems[i].getAsEntry;
        var entry = getAsEntry ? getAsEntry.call(dtItems[i]) : null;
        if (entry) entries.push(entry);
      }
    }
    if (!entries.length) {
      return Promise.resolve(filesToItems(dataTransfer.files));
    }
    return Promise.all(entries.map(function (e) { return walkEntry(e, ''); }))
      .then(function (lists) { return Array.prototype.concat.apply([], lists); });
  }

  document.querySelectorAll('[data-dropzone]').forEach(function (form) {
    var fileInput = form.querySelector('input[type=file]');
    if (!fileInput) return;
    var folderCapable = form.hasAttribute('data-folder-capable');

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
      if (!e.dataTransfer) return;
      if (folderCapable) {
        dropToItems(e.dataTransfer).then(function (items) {
          if (items.length) uploadItemsSequential(form, items);
        });
        return;
      }
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
      if (!fileInput.files.length) return;
      if (folderCapable) uploadItemsSequential(form, filesToItems(fileInput.files));
      else startUpload(form);
    });

    // Folder-capable dropzones also expose a hidden webkitdirectory input
    // triggered by an inline "select a whole folder…" button, so folders
    // work by click as well as drag.
    if (folderCapable) {
      var folderInput = form.querySelector('[data-folder-input]');
      var folderPick = form.querySelector('[data-folder-pick]');
      if (folderPick && folderInput) {
        folderPick.addEventListener('click', function () { folderInput.click(); });
      }
      if (folderInput) {
        folderInput.addEventListener('change', function () {
          if (folderInput.files.length) {
            uploadItemsSequential(form, filesToItems(folderInput.files));
          }
        });
      }
    }
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
  // synthetic input event for any client-side filter listening. List
  // search forms are skipped: they have their own Clear link, and
  // editing the box must never navigate by itself.
  document.querySelectorAll('.filter').forEach(function (el) {
    if (el.matches('form[data-search-form]')) return;
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
