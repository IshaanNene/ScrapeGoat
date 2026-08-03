/* ScrapeGoat site behaviour.
 *
 * Four small things: theme, mobile drawer, copy buttons, tabs. No framework and
 * no dependencies — the page is readable and navigable with this file missing
 * entirely, which is the property that matters for documentation.
 */

(() => {
  'use strict';

  /* ── Theme ─────────────────────────────────────────────────────
   * Explicit choice wins over the OS preference and persists. The initial value
   * is set on <html> in the markup so there is no flash of the wrong theme
   * before this script runs. */

  const root = document.documentElement;
  const STORE = 'sg-theme';

  const stored = (() => {
    try { return localStorage.getItem(STORE); } catch { return null; }
  })();

  if (stored === 'light' || stored === 'dark') {
    root.dataset.theme = stored;
  } else if (window.matchMedia?.('(prefers-color-scheme: light)').matches) {
    root.dataset.theme = 'light';
  }

  document.getElementById('theme')?.addEventListener('click', () => {
    const next = root.dataset.theme === 'light' ? 'dark' : 'light';
    root.dataset.theme = next;
    try { localStorage.setItem(STORE, next); } catch { /* private mode */ }
  });

  // Follow the OS only while the visitor has not chosen for themselves.
  window.matchMedia?.('(prefers-color-scheme: light)').addEventListener?.('change', e => {
    let chosen = null;
    try { chosen = localStorage.getItem(STORE); } catch { /* ignore */ }
    if (!chosen) root.dataset.theme = e.matches ? 'light' : 'dark';
  });

  /* ── Mobile drawer ─────────────────────────────────────────── */

  const menu = document.getElementById('menu');
  const drawer = document.getElementById('drawer');

  const setDrawer = open => {
    if (!drawer || !menu) return;
    drawer.hidden = !open;
    menu.setAttribute('aria-expanded', String(open));
  };

  menu?.addEventListener('click', () => setDrawer(drawer.hidden));
  drawer?.addEventListener('click', e => {
    if (e.target.tagName === 'A') setDrawer(false);
  });
  document.addEventListener('keydown', e => {
    if (e.key === 'Escape') setDrawer(false);
  });

  /* ── Copy buttons ──────────────────────────────────────────── */

  document.querySelectorAll('[data-copy]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const text = btn.dataset.copy;
      try {
        await navigator.clipboard.writeText(text);
      } catch {
        // Clipboard API needs a secure context and permission; fall back rather
        // than leaving the button looking broken.
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); } catch { /* give up quietly */ }
        ta.remove();
      }

      const original = btn.textContent;
      btn.textContent = 'Copied';
      btn.dataset.done = '';
      setTimeout(() => {
        btn.textContent = original;
        delete btn.dataset.done;
      }, 1400);
    });
  });

  /* ── Tabs ──────────────────────────────────────────────────────
   * Follows the ARIA authoring practice: arrow keys move between tabs, Home and
   * End jump to the ends, and only the active tab is in the tab order. Panels
   * are plain markup, so with JS disabled the first one still shows. */

  document.querySelectorAll('[data-tabs]').forEach(group => {
    const tabs = [...group.querySelectorAll('[role="tab"]')];
    if (!tabs.length) return;

    const select = idx => {
      tabs.forEach((tab, i) => {
        const on = i === idx;
        tab.setAttribute('aria-selected', String(on));
        tab.tabIndex = on ? 0 : -1;

        const panel = document.getElementById(tab.getAttribute('aria-controls'));
        if (panel) panel.hidden = !on;
      });
    };

    tabs.forEach((tab, i) => {
      tab.addEventListener('click', () => select(i));

      tab.addEventListener('keydown', e => {
        const last = tabs.length - 1;
        let next = null;

        switch (e.key) {
          case 'ArrowRight': next = i === last ? 0 : i + 1; break;
          case 'ArrowLeft':  next = i === 0 ? last : i - 1; break;
          case 'Home':       next = 0; break;
          case 'End':        next = last; break;
          default: return;
        }

        e.preventDefault();
        select(next);
        tabs[next].focus();
      });
    });
  });
})();
