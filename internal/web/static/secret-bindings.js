(() => {
  const panel = document.getElementById('secret-bindings');
  if (!panel) return;
  const stack = panel.dataset.stack;
  const base = `/api/stacks/${encodeURIComponent(stack)}/secret-bindings`;
  const rows = document.getElementById('secret-rows');
  const message = document.getElementById('secret-message');
  const editor = document.getElementById('secret-editor');
  const plan = document.getElementById('secret-plan');
  const confirm = document.getElementById('secret-confirm');
  const buttons = [...editor.querySelectorAll('button')];
  let target = '', token = '', dirty = false, busy = false;
  function say(s) { message.textContent = s; }
  function invalidate() { token = ''; confirm.disabled = true; plan.textContent = ''; }
  function setBusy(value) { busy = value; buttons.forEach(b => b.disabled = value); rows.querySelectorAll('input, button').forEach(el => el.disabled = value); confirm.disabled = value || !token || dirty; }
  function addRow(binding = {}) {
    const row = document.createElement('div'); row.className = 'flex flex-wrap gap-2';
    for (const key of ['name', 'source', 'revision']) {
      const input = document.createElement('input'); input.type = 'text'; input.name = key; input.value = binding[key] || '';
      input.placeholder = key === 'name' ? 'logical name' : key === 'source' ? 'opaque source ID' : 'revision';
      input.setAttribute('aria-label', input.placeholder); input.maxLength = 63; input.className = 'ui-input ui-input-sm font-mono';
      input.addEventListener('input', () => { dirty = true; invalidate(); }); row.appendChild(input);
    }
    const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'ui-button ui-button-ghost ui-button-sm'; remove.textContent = 'Remove';
    remove.addEventListener('click', () => { row.remove(); dirty = true; invalidate(); }); row.appendChild(remove); rows.appendChild(row);
  }
  async function request(suffix, method, body) {
    const options = {method, headers: brineHeaders(body === undefined ? {} : {'Content-Type':'application/json'})};
    if (body !== undefined) options.body = JSON.stringify(body);
    const response = await fetch(base + suffix, options);
    let data; try { data = await response.json(); } catch { throw new Error('Secret workflow unavailable.'); }
    if (!response.ok) throw new Error(data.error || 'Secret workflow unavailable.');
    return data;
  }
  async function run(fn) { if (busy) return; setBusy(true); try { await fn(); } catch (e) { say(e.message || 'Secret workflow unavailable.'); } finally { setBusy(false); } }
  function fill(doc) { rows.replaceChildren(); for (const binding of (doc?.bindings || [])) addRow(binding); }
  async function refresh() {
    const data = await request('', 'GET'); target = data.target;
    document.getElementById('secret-active').textContent = data.active ? JSON.stringify(data.active.bindings, null, 2) : 'No approved binding.';
    fill(data.draft); dirty = false; invalidate(); editor.hidden = false; say('Draft and active references are separate.');
  }
  document.getElementById('secret-add').addEventListener('click', () => { if (!busy) { addRow(); dirty = true; invalidate(); } });
  document.getElementById('secret-save').addEventListener('click', () => run(async () => {
    const bindings = [...rows.children].map(row => Object.fromEntries([...row.querySelectorAll('input')].map(input => [input.name, input.value])));
    await request('', 'PUT', {version:'dill.secret-bindings/1', target, stack, bindings}); dirty = false; invalidate(); say('Draft saved. Preview and confirm before it can be applied.');
  }));
  document.getElementById('secret-status').addEventListener('click', () => run(async () => {
    if (dirty) throw new Error('Save the draft before checking availability.');
    const data = await request('/status', 'POST'); say(data.status === 'available' ? 'Draft sources available now.' : 'Secret configuration or source unavailable.');
  }));
  document.getElementById('secret-active-status').addEventListener('click', () => run(async () => {
    const data = await request('/status', 'GET'); say(data.status === 'available' ? 'Active sources available now.' : 'Secret configuration or source unavailable.');
  }));
  document.getElementById('secret-preview').addEventListener('click', () => run(async () => {
    if (dirty) throw new Error('Save the draft before previewing.');
    invalidate(); const data = await request('/preview', 'POST'); token = data.preview_token; plan.textContent = JSON.stringify(data.plan, null, 2); say('Review the plan, then explicitly confirm.');
  }));
  confirm.addEventListener('click', () => run(async () => {
    if (!token || dirty) throw new Error('Preview the saved draft again.');
    if (!window.confirm('Apply exactly this previewed reference change? Containers may be recreated.')) return;
    const approved = token; invalidate(); await request('/confirm', 'POST', {preview_token:approved}); await refresh(); say('Approved references applied.');
  }));
  run(refresh);
})();
