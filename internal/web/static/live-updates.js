(function () {
  const key = 'brine-live-updates';
  const defaults = { enabled: true, interval: 5 };
  const intervals = [5, 10, 30, 60];
  const byId = id => document.getElementById(id);

  function stored() {
    try {
      const value = JSON.parse(localStorage.getItem(key));
      return {
        enabled: typeof value?.enabled === 'boolean' ? value.enabled : defaults.enabled,
        interval: intervals.includes(value?.interval) ? value.interval : defaults.interval,
      };
    } catch (_) { return { ...defaults }; }
  }

  function init() {
    const auto = byId('auto-refresh'), interval = byId('refresh-interval');
    const row = byId('interval-row'), feedback = byId('refresh-feedback');
    const panel = byId('panel-Containers'), inventory = byId('stack-containers');
    let prefs = stored(), timer = 0, first = true, paused = false, request;

    function eligible() {
      return panel && inventory?.isConnected && !paused && !panel.hidden &&
        document.visibilityState === 'visible' && !document.querySelector('dialog[open]') &&
        !inventory.contains(document.activeElement);
    }
    function clearTimer() { clearTimeout(timer); timer = 0; }
    function schedule() {
      const visible = eligible();
      if (request?.automatic && (!visible || (!request.initial && !prefs.enabled))) request.discard = true;
      if (!visible || request || (!first && !prefs.enabled)) { clearTimer(); return; }
      if (!timer) timer = setTimeout(() => {
        timer = 0;
        if (eligible() && !request && (first || prefs.enabled)) window.htmx?.trigger(inventory, 'stack-inventory');
      }, first ? 0 : prefs.interval * 1000);
    }
    function apply() {
      if (auto) auto.checked = prefs.enabled;
      if (interval) interval.value = String(prefs.interval);
      if (row) row.hidden = !prefs.enabled;
      clearTimer();
      schedule();
    }
    function save(message, reset = false) {
      let saved = true;
      try {
        if (reset) localStorage.removeItem(key);
        else localStorage.setItem(key, JSON.stringify(prefs));
      } catch (_) { saved = false; }
      apply();
      if (feedback) feedback.textContent = message + (saved ? ' Saved for this browser.' : ' Applied in this tab only.');
    }
    auto?.addEventListener('change', () => {
      prefs.enabled = auto.checked;
      save(prefs.enabled ? 'Automatic refresh on.' : 'Automatic refresh off.');
    });
    interval?.addEventListener('change', () => {
      const seconds = Number(interval.value);
      if (!intervals.includes(seconds)) return;
      prefs.interval = seconds;
      save(`Refresh interval set to ${seconds} seconds.`);
    });
    byId('reset-live-updates')?.addEventListener('click', event => {
      event.preventDefault();
      prefs = { ...defaults };
      save('Live updates reset.', true);
    });
    window.addEventListener('storage', event => {
      if (event.key !== key && event.key !== null) return;
      try { if (event.storageArea && event.storageArea !== localStorage) return; }
      catch (_) { return; }
      prefs = stored();
      apply();
    });
    apply();
    if (!panel || !inventory) return;

    // Track only inventory reads. A manual read can supersede a pending background read.
    document.addEventListener('htmx:beforeRequest', event => {
      const { elt, target, xhr } = event.detail;
      const automatic = elt === inventory;
      if (target !== inventory || (!automatic && !['stack-containers-refresh', 'stack-containers-retry'].includes(elt.id))) return;
      if (event.defaultPrevented || (automatic && (!eligible() || (!first && !prefs.enabled))) ||
          (request && (automatic || !request.automatic || request.responding))) {
        event.preventDefault();
        return;
      }
      const previous = request;
      request = { xhr, automatic, initial: first };
      first = false;
      clearTimer();
      if (!xhr.timeout) xhr.timeout = 10000;
      previous?.xhr.abort();
    });
    const matches = event => request && event.detail.xhr === request.xhr;
    document.addEventListener('htmx:beforeSwap', event => {
      if (!matches(event)) return;
      request.responding = true;
      if (request.xhr.responseURL && new URL(request.xhr.responseURL).pathname === '/login') {
        event.detail.shouldSwap = false;
        return;
      }
      if (request.automatic && (request.discard || !eligible() || (!request.initial && !prefs.enabled))) {
        event.detail.shouldSwap = false;
        if (request.initial) first = true;
      }
    });
    document.addEventListener('htmx:afterSwap', event => { if (matches(event)) request.swapped = true; });
    function finish(event) {
      if (!matches(event)) return;
      request = undefined;
      schedule();
    }
    document.addEventListener('htmx:afterSettle', finish);
    document.addEventListener('htmx:afterRequest', event => {
      if (!matches(event) || request.swapped) return;
      if (!request.discard && (request.xhr.status !== 200 || !request.xhr.responseURL || new URL(request.xhr.responseURL).pathname === '/login')) {
        // A failed read never leaves a previously running service presented as current.
        // Keep an existing error/retry control (and its focus) through a failed retry.
        if (!inventory.querySelector('[role="alert"]')) {
          const error = document.createElement('p');
          error.setAttribute('role','alert'); error.className = 'px-5 py-4 text-error';
          error.textContent = 'Stack container inventory is unavailable. Use Refresh to read it again.';
          inventory.replaceChildren(error);
        }
      }
      finish(event);
    });
    document.addEventListener('htmx:load', schedule);
    window.addEventListener('brine:stack-tab-change', schedule, true);
    document.addEventListener('visibilitychange', schedule);
    document.addEventListener('focusin', schedule);
    document.addEventListener('focusout', () => queueMicrotask(schedule));
    new MutationObserver(schedule).observe(document.body, {
      subtree: true, childList: true, attributes: true, attributeFilter: ['hidden', 'open'],
    });
    window.addEventListener('pagehide', () => { paused = true; schedule(); });
    window.addEventListener('pageshow', () => { paused = false; schedule(); });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
