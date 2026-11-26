(() => {
  function init() {
    const list = document.querySelector('[role="tablist"][aria-label="Stack sections"]');
    if (!list) return;
    const tabs = [...list.querySelectorAll('[role="tab"][data-stack-tab]')];
    const tab = name => tabs.find(item => item.dataset.stackTab === name);
    const planTab = tab('Plan');
    const button = document.getElementById('plan-button');
    const result = document.getElementById('plan-result');
    const legacy = { '#history': 'history', '#variables': 'variables' };
    let active, pending;

    function select(next, focus = false) {
      if (!next || next.hidden) next = tab('Compose');
      const name = next.dataset.stackTab;
      for (const item of tabs) {
        const selected = item === next;
        item.setAttribute('aria-selected', String(selected));
        item.tabIndex = selected ? 0 : -1;
        document.getElementById(item.getAttribute('aria-controls')).hidden = !selected;
      }
      const url = new URL(location.href);
      if (name === 'Compose') url.searchParams.delete('tab');
      else url.searchParams.set('tab', name.toLowerCase());
      if (legacy[url.hash]) url.hash = '';
      if (url.href !== location.href) history.replaceState(history.state, '', url);
      if (focus) next.focus({ preventScroll: true });
      if (next === active) return;
      active = next;
      list.dispatchEvent(new CustomEvent('brine:stack-tab-change', { bubbles: true, detail: { tab: name } }));
      if (name === 'History') {
        const content = document.getElementById('stack-history-content');
        content?.dispatchEvent(new CustomEvent('load-history', { bubbles: true }));
      }
    }
    function fromURL() {
      const url = new URL(location.href);
      const name = list.dataset.initialTab || legacy[url.hash] || url.searchParams.get('tab')?.toLowerCase();
      delete list.dataset.initialTab;
      select(tabs.find(item => item.dataset.stackTab.toLowerCase() === name));
    }
    list.addEventListener('click', event => {
      const next = event.target.closest('[data-stack-tab]');
      if (tabs.includes(next)) select(next, true);
    });
    list.addEventListener('keydown', event => {
      const visible = tabs.filter(item => !item.hidden);
      const index = visible.indexOf(event.target.closest('[data-stack-tab]'));
      if (index < 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      const direction = getComputedStyle(list).direction === 'rtl' ? -1 : 1;
      const indices = { ArrowLeft: index - direction, ArrowRight: index + direction, Home: 0, End: visible.length - 1 };
      if (!Object.hasOwn(indices, event.key)) return;
      event.preventDefault();
      select(visible[(indices[event.key] + visible.length) % visible.length], true);
    });
    document.addEventListener('click', event => {
      if (!event.target.closest('[data-open-history]')) return;
      event.preventDefault();
      if (active === tab('History')) document.getElementById('stack-history-content').dispatchEvent(new CustomEvent('load-history', {bubbles:true}));
      select(tab('History'), true);
    });
    window.addEventListener('hashchange', fromURL);
    window.addEventListener('popstate', fromURL);

    if (button && result && planTab) {
      const deploy = document.getElementById('deploy-button');
      const policyDisabled = deploy.disabled;
      const stale = document.getElementById('plan-stale');
      const tools = document.getElementById('plan-tools');
      const empty = () => !result.childElementCount && !result.textContent.trim();
      function dismiss() {
        const xhr = pending;
        pending = null;
        xhr?.abort();
        result.replaceChildren();
        planTab.hidden = true;
        if (active === planTab) select(tab('Compose'), true);
      }
      function markStale(force) {
        if (planTab.hidden && !force && !deploy.dataset.planInputs) return;
        const xhr = pending;
        pending = null;
        xhr?.abort();
        if (empty() || result.querySelector('.stack-loading')) result.textContent = 'No current plan was returned.';
        planTab.hidden = false;
        stale.hidden = false;
        deploy.dataset.needsPlan = 'true';
        deploy.disabled = true;
      }
      document.addEventListener('brine:stale-plan', () => { markStale(true); select(planTab, true); });
      document.getElementById('dismiss-plan').addEventListener('click', dismiss);
      // Returning to a cached document cannot attest that its inputs survived.
      window.addEventListener('pageshow', e => { if (e.persisted) markStale(false); });
      document.addEventListener('htmx:beforeRequest', event => {
        const { elt, xhr, requestConfig } = event.detail;
        if (elt !== button) {
          if (requestConfig?.verb !== 'get' && elt?.closest('#git-source-panel, #git-source-actions')) markStale(false);
          return;
        }
        if (button.disabled) { event.preventDefault(); return; }
        pending = xhr;
        result.setAttribute('role', 'status');
        result.innerHTML = '<div class="stack-loading"><span class="ui-spinner" aria-hidden="true"></span><span>Generating plan…</span></div>';
        tools.hidden = false;
        planTab.hidden = false;
        select(planTab, true);
      });
      document.addEventListener('htmx:beforeSwap', event => {
        const { target, xhr } = event.detail;
        if (target === result && (xhr !== pending || xhr.status !== 200 || new URL(xhr.responseURL).pathname === '/login')) {
          event.detail.shouldSwap = false;
        }
      });
      document.addEventListener('htmx:afterRequest', event => {
        const xhr = event.detail.xhr;
        if (!pending || xhr !== pending) return;
        pending = null;
        if (xhr.status === 409 && xhr.getResponseHeader('X-Brine-Plan-Stale') === 'true') {
          markStale(true);
          return;
        }
        if (result.querySelector('.stack-loading') || empty()) {
          result.textContent = 'Plan unavailable. No current plan was returned.';
        } else if (!result.querySelector('.ui-notice-error,.ui-notice-warning')) {
          const tag = xhr.getResponseHeader('X-Brine-Plan-Inputs');
          if (tag && /^[0-9a-f]{64}$/.test(tag)) {
            deploy.dataset.planInputs = tag;
            delete deploy.dataset.needsPlan;
            deploy.disabled = policyDisabled;
            stale.hidden = true;
          }
          result.removeAttribute('role');
        }
        tools.hidden = !!result.querySelector('button[aria-label="Close plan"]');
      });
      // Lifecycle handlers cancel outstanding reads and clear the diff; they
      // do not grant authority through re-enabling originally disabled buttons.
      new MutationObserver(() => { if (!pending && empty() && !planTab.hidden && stale.hidden) dismiss(); }).observe(result, { childList: true });
    }

    // The History read/review is replaceable. Never leave an old Confirm
    // button on-screen as though it came from a newer failed review.
    const historyContent = document.getElementById('stack-history-content');
    function historyTarget(event) {
      const target = event.detail.target;
      return target === historyContent || target?.id === 'stack-source-history';
    }
    function historyMessage(el, msg, loading = false) {
      const row = document.createElement('div');
      row.className = loading ? 'stack-loading' : 'ui-panel p-5';
      row.setAttribute('role', loading ? 'status' : 'alert');
      if (loading) {
        const spinner = document.createElement('span');
        spinner.className = 'ui-spinner'; spinner.setAttribute('aria-hidden', 'true'); row.append(spinner);
      }
      row.append(document.createTextNode(msg));
      el.replaceChildren(row);
    }
    // beforeSend: HTMX already gathered and bound the original form controls.
    document.addEventListener('htmx:beforeSend', event => {
      if (!historyTarget(event)) return;
      const {target, xhr} = event.detail;
      const path = event.detail.requestConfig.path;
      const confirm = path.endsWith('/confirm');
      xhr.addEventListener('loadend', () => {
        if (!target.isConnected) return;
        if (confirm && xhr.status === 204 && xhr.getResponseHeader('HX-Redirect')) return;
        if (confirm || xhr.status !== 200 || !xhr.responseURL || new URL(xhr.responseURL).pathname === '/login') {
          const detail = xhr.status === 409 && xhr.getResponseHeader('Content-Type')?.startsWith('text/plain') ? xhr.responseText.trim().slice(0, 300) : '';
          historyMessage(target, detail || (confirm ? 'Source publication was not confirmed. Inspect current source and History. Do not resubmit.' : 'History or review unavailable. Check your session and reopen History. No restore was requested.'));
        }
      }, {once:true});
      // Keep the HTMX request's source attached through its lifecycle.
      if (target === historyContent) historyMessage(target, 'Loading history…', true);
      else if (confirm || path.endsWith('/review')) {
        target.querySelectorAll('button').forEach(b => { b.disabled = true; });
        const status = document.createElement('div');
        historyMessage(status, confirm ? 'Publishing source revision…' : 'Verifying source…', true); target.prepend(status);
      }
    });
    historyContent?.addEventListener('htmx:beforeSwap', event => {
      const xhr = event.detail.xhr;
      if (historyTarget(event) && xhr.responseURL && new URL(xhr.responseURL).pathname === '/login') event.detail.shouldSwap = false;
    });
    historyContent?.addEventListener('htmx:afterSettle', event => {
      if (event.detail.requestConfig?.path?.endsWith('/review') && active === tab('History')) {
        historyContent.querySelector('#source-review-heading')?.focus({preventScroll:true});
      }
    });

    const timeSelector = 'time[data-relative][datetime]';
    const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'always' });
    const exact = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' });
    const units = [['year', 31536000], ['month', 2592000], ['day', 86400], ['hour', 3600], ['minute', 60], ['second', 1]];
    const sibling = time => time.nextElementSibling?.matches('.exact-time') ? time.nextElementSibling : null;
    function formatTimes(root) {
      const times = root.matches?.(timeSelector) ? [root] : root.querySelectorAll(timeSelector);
      for (const time of times) {
        const date = new Date(time.dateTime);
        if (!Number.isFinite(date.getTime())) continue;
        const label = sibling(time)?.textContent.trim() || exact.format(date);
        const seconds = (date.getTime() - Date.now()) / 1000;
        const [unit, size] = units.find(([, n]) => Math.abs(seconds) >= n) || units[units.length - 1];
        time.textContent = Math.abs(seconds) < 10 ? 'just now' : relative.format(Math.trunc(seconds / size), unit);
        time.title = label;
        time.setAttribute('aria-label', label);
        if (sibling(time)) time.tabIndex = 0;
      }
    }
    function reveal(event, show) {
      if (!event.target.matches(timeSelector)) return;
      const detail = sibling(event.target);
      if (!detail) return;
      detail.hidden = !show;
      detail.classList.toggle('hidden', !show);
    }
    document.addEventListener('focusin', event => reveal(event, true));
    document.addEventListener('focusout', event => reveal(event, false));
    document.addEventListener('click', event => {
      if (event.target.matches(timeSelector) && sibling(event.target)) event.target.focus({ preventScroll: true });
      reveal(event, true);
    });
    document.addEventListener('htmx:afterSwap', event => formatTimes(event.detail.target));
    formatTimes(document);
    fromURL();
  }

  // This deferred script runs in the interactive state, before HTMX binds
  // load-history during DOMContentLoaded. Do not lose the initial deep-link read.
  if (document.readyState !== 'complete') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
