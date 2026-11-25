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
      const name = legacy[url.hash] || url.searchParams.get('tab')?.toLowerCase();
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
      select(tab('History'), true);
    });
    window.addEventListener('hashchange', fromURL);
    window.addEventListener('popstate', fromURL);

    if (button && result && planTab) {
      const empty = () => !result.childElementCount && !result.textContent.trim();
      function hidePlan() {
        planTab.hidden = true;
        if (active === planTab) select(tab('Compose'), true);
      }
      function invalidate() {
        const xhr = pending;
        pending = null;
        result.replaceChildren();
        result.removeAttribute('role');
        hidePlan();
        xhr?.abort();
      }
      // Emit from lifecycle/Git handlers that mutate state outside HTMX.
      document.addEventListener('brine:invalidate-plan', invalidate);
      if (!button.hasAttribute('hx-sync')) button.setAttribute('hx-sync', 'this:replace');
      document.addEventListener('htmx:beforeRequest', event => {
        const { elt, xhr, requestConfig, target } = event.detail;
        if (elt !== button) {
          const git = '#git-source-panel, #git-source-actions';
          if (/^(post|put|patch|delete)$/i.test(requestConfig?.verb) && (elt?.closest(git) || target?.closest(git))) invalidate();
          return;
        }
        if (button.disabled) { event.preventDefault(); return; }
        pending = xhr;
        result.setAttribute('role', 'status');
        result.innerHTML = '<div class="stack-loading flex items-center gap-2"><span class="ui-spinner" aria-hidden="true"></span><span>Generating plan…</span></div>';
        planTab.hidden = false;
        select(planTab, true);
      });
      document.addEventListener('htmx:beforeSwap', event => {
        const { target, xhr } = event.detail;
        if (target === result && (xhr !== pending || xhr.status < 200 || xhr.status >= 300)) event.preventDefault();
      });
      document.addEventListener('htmx:afterRequest', event => {
        if (!pending || event.detail.xhr !== pending) return;
        pending = null;
        if (result.querySelector('.stack-loading') || empty()) {
          result.setAttribute('role', 'status');
          result.textContent = 'Plan unavailable. No current plan was returned.';
        } else result.removeAttribute('role');
      });
      // The plan partial's native close removes its own panel.
      new MutationObserver(() => { if (!pending && empty()) hidePlan(); }).observe(result, { childList: true });
      new MutationObserver(() => { if (button.disabled) invalidate(); }).observe(button, { attributes: true, attributeFilter: ['disabled'] });
      planTab.hidden = empty();
    }

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

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
