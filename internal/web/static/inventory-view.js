for (const view of document.querySelectorAll('[data-inventory-view]')) {
  const controls = view.querySelector('[data-inventory-controls]');
  const body = document.getElementById(controls.dataset.inventoryControls);
  const footer = view.querySelector('[data-inventory-result]');
  const summary = footer.querySelector('[data-inventory-summary]');
  const search = controls.querySelector('[data-inventory-search]');
  const filter = controls.querySelector('[data-inventory-filter]');
  const sort = controls.querySelector('[data-inventory-sort]');
  const prev = footer.querySelector('[data-inventory-prev]');
  const next = footer.querySelector('[data-inventory-next]');
  const pageSize = 10;
  const primaryStates = ['running', 'exited', 'paused'];
  const engineOrder = new Map();
  let page = 1;
  function setPageButtons(previous, following) {
    prev.setAttribute('aria-disabled', String(!previous));
    next.setAttribute('aria-disabled', String(!following));
  }

  function render(refreshOrder = false) {
    const rows = Array.from(body.querySelectorAll('[data-inventory-row]'));
    if (refreshOrder) engineOrder.clear();
    rows.forEach(row => {
      if (!engineOrder.has(row.id)) engineOrder.set(row.id, engineOrder.size);
    });
    if (!rows.length) {
      const focus = document.activeElement;
      if (controls.contains(focus) || footer.contains(focus)) {
        const fallback = body.querySelector('button[hx-get]') || view.querySelector('h1');
        fallback.focus();
      }
      controls.hidden = footer.hidden = true;
      summary.textContent = '';
      setPageButtons(false, false);
      return;
    }
    controls.hidden = footer.hidden = false;

    const query = search.value.trim().toLowerCase();
    const mode = sort.value;
    const ordered = [...rows].sort((a, b) => {
      let difference = 0;
      if (mode === 'name' || mode === 'reverse') difference = a.dataset.name.localeCompare(b.dataset.name) * (mode === 'reverse' ? -1 : 1);
      if (mode === 'newest' || mode === 'largest') difference = Number(b.dataset[mode]) - Number(a.dataset[mode]);
      return difference || engineOrder.get(a.id) - engineOrder.get(b.id);
    });
    // Move only out-of-order rows; retain the nodes and their HTMX bindings.
    let cursor = body.firstElementChild;
    for (const row of ordered) {
      if (row !== cursor) body.insertBefore(row, cursor);
      cursor = row.nextElementSibling;
    }
    const matches = ordered.filter(row => {
      const state = row.dataset.filter;
      const selected = !filter.value || (filter.value === 'other' ? !primaryStates.includes(state) : state === filter.value);
      const searchable = Array.from(row.cells).slice(0, -1).map(cell => cell.textContent).join(' ');
      return selected && (!query || row.id.toLowerCase().includes(query) || searchable.toLowerCase().includes(query));
    });
    const pages = Math.max(1, Math.ceil(matches.length / pageSize));
    page = Math.min(page, pages);
    const offset = (page - 1) * pageSize;
    const visible = matches.slice(offset, offset + pageSize);
    const visibleRows = new Set(visible);
    ordered.forEach(row => { row.hidden = !visibleRows.has(row); });
    const message = matches.length
      ? `Showing ${offset + 1}–${offset + visible.length} of ${matches.length} · Page ${page} of ${pages}`
      : 'No matching results. Change the search or filter.';
    if (summary.textContent !== message) summary.textContent = message;
    setPageButtons(page > 1, page < pages);
  }

  function resetPage() { page = 1; render(); }
  search.addEventListener('input', resetPage);
  filter.addEventListener('change', resetPage);
  sort.addEventListener('change', resetPage);
  function turnPage(delta, trigger) {
    if (trigger.getAttribute('aria-disabled') === 'true') return;
    page += delta;
    render();
  }
  prev.addEventListener('click', () => turnPage(-1, prev));
  next.addEventListener('click', () => turnPage(1, next));

  // Do not replace rows during background polling while someone is operating
  // them with the keyboard. Explicit retries and actions still run normally.
  body.addEventListener('htmx:beforeRequest', event => {
    if (event.detail.elt === body && body.id === 'containers-body' && body.contains(document.activeElement)) event.preventDefault();
  });
  let actionFocus;
  body.addEventListener('htmx:beforeSwap', event => {
    const row = event.detail.target;
    if (row === body && event.detail.requestConfig?.elt === body && body.contains(document.activeElement)) {
      event.detail.shouldSwap = false;
    } else if (row.matches('[data-inventory-row]') && row.contains(document.activeElement)) {
      actionFocus = row.id;
    }
  });
  document.body.addEventListener('htmx:afterSwap', event => {
    if (!actionFocus || event.detail.target?.id !== actionFocus) return;
    const row = document.getElementById(actionFocus);
    actionFocus = undefined;
    row?.cells[0]?.querySelector('a')?.focus({ preventScroll: true });
  });
  new MutationObserver(() => render()).observe(body, { childList: true });
  body.addEventListener('htmx:afterSwap', event => { if (event.detail.target === body) render(true); });
  render(true);
}
