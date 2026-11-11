const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.resolve(__dirname, '..');
const read = file => fs.readFileSync(path.join(root, file), 'utf8');
function files(dir, extensions) {
  return fs.readdirSync(path.join(root, dir), { withFileTypes: true }).flatMap(entry => {
    const name = path.posix.join(dir, entry.name);
    if (entry.isDirectory()) return files(name, extensions);
    return extensions.some(ext => name.endsWith(ext)) && !name.endsWith('_test.go') ? [name] : [];
  });
}

// Match complete class tokens, not tag names, identifiers, IDs, or select-none.
const oldClass = /^(?:btn(?:-(?:primary|ghost|outline|error|success|warning|sm|xs))?|badge(?:-(?:success|warning|error|info|primary|ghost|outline|sm|xs))?|alert(?:-(?:error|success|warning|info|sm))?|input(?:-(?:sm|xs|bordered))?|textarea(?:-bordered)?|select(?:-(?:sm|xs|bordered))?|checkbox(?:-(?:sm|error|warning|primary))?|toggle(?:-(?:xs|success))?|loading(?:-(?:sm|xs|spinner))?|table(?:-sm)?|form-control|label(?:-text(?:-alt)?)?|modal(?:-(?:box|action|backdrop))?|toast(?:-(?:top|end))?|divider|rounded-box|(?:bg|border(?:-b)?|divide)-base-(?:100|200|300)(?:\/\d+)?|text-base-content(?:\/\d+)?|(?:text|bg|border)-primary(?:\/\d+)?)$/;
function checkTokens(file, value) {
  for (const token of value.split(/\s+/)) {
    const unmodified = token.split(':').pop();
    assert.ok(!oldClass.test(unmodified), `${file}: obsolete class ${token}`);
    assert.doesNotMatch(unmodified, /^text-foreground\/\d+$/, `${file}: use contrast-tested muted/dim tokens, not low-opacity text`);
  }
}

test('production dependencies and Tailwind config do not load DaisyUI', () => {
  const pkg = JSON.parse(read('package.json'));
  assert.ok(!('daisyui' in { ...pkg.dependencies, ...pkg.devDependencies, ...pkg.optionalDependencies }));
  const lock = JSON.parse(read('package-lock.json'));
  assert.ok(!Object.keys(lock.packages || {}).some(key => /(?:^|\/)daisyui$/.test(key)));
  assert.doesNotMatch(read('tailwind.config.js'), /\bdaisyui\b/i);
});

test('production class-bearing strings do not use DaisyUI component or color classes', () => {
  const sources = [
    ...files('internal/web/templates', ['.html']),
    ...files('internal/web', ['.go']),
    'internal/web/static/ui.js', 'internal/web/assets/editor.js', 'internal/auth/auth.go',
  ];
  for (const file of sources) {
    const source = read(file);
    // HTML/Go fragment class attributes, including JS HTML template literals.
    for (const match of source.matchAll(/\bclass\s*=\s*["']([^"']*)["']/g)) checkTokens(file, match[1]);
    // Dynamic class assignments and classList operations, not arbitrary JS strings.
    for (const match of source.matchAll(/(?:\.className\s*=|\.classList\.(?:add|remove|toggle|replace)\s*\()\s*(["'`])([\s\S]*?)\1/g)) checkTokens(file, match[2]);
  }
});

test('source styles use Brine tokens and not DaisyUI selectors or theme variables', () => {
  const css = read('internal/web/assets/app.css');
  assert.doesNotMatch(css, /--(?:b1|b2|b3|bc|p|pc|su|wa|er|in)\b/);
  for (const match of css.matchAll(/\.([a-z][a-z0-9-]*)(?=[\s:{.#>+~,\[])/g)) {
    assert.ok(!oldClass.test(match[1]), `obsolete CSS selector .${match[1]}`);
  }
  const theme = read('internal/web/static/theme.css');
  for (const token of ['color-bg', 'color-panel', 'color-raised', 'color-line', 'color-text', 'color-muted', 'color-dim', 'color-accent', 'color-accent-contrast', 'status-success', 'status-warning', 'status-error', 'status-info', 'font-family', 'radius-control', 'control-height']) {
    assert.match(theme, new RegExp(`--${token}\\s*:`), `missing --${token}`);
  }
  const layout = read('internal/web/templates/layout.html');
  assert.ok(layout.indexOf('/static/theme.css') >= 0 && layout.indexOf('/static/theme.css') < layout.indexOf('/static/vendor/app.css'));
});

// Exercise preference behavior without a browser, server or third-party DOM.
function themeHarness(initial = {}, { dark = false, failReads = false, failWrites = false } = {}) {
  const values = new Map(Object.entries(initial));
  const listeners = {};
  const docListeners = {};
  const controls = {
    'theme-select': {}, 'density-select': {},
    'preference-storage-warning': {}, 'preference-feedback': {},
  };
  const radios = ['sage', 'glacier', 'lilac', 'sand', 'rose', 'graphite'].map(value => ({ value }));
  const root = { dataset: {}, style: {} };
  const storage = {
    getItem(key) { if (failReads) throw new Error('storage disabled'); return values.get(key) ?? null; },
    setItem(key, value) { if (failWrites) throw new Error('storage full'); values.set(key, value); },
    removeItem(key) { if (failWrites) throw new Error('storage disabled'); values.delete(key); },
  };
  let mediaChanged;
  const media = { matches: dark, addEventListener(type, callback) { mediaChanged = callback; } };
  const events = [];
  const window = {
    matchMedia: () => media,
    addEventListener(type, callback) { listeners[type] = callback; },
    dispatchEvent(event) { events.push(event); },
  };
  const document = {
    documentElement: root,
    getElementById: id => controls[id] || null,
    querySelectorAll: () => radios,
    addEventListener(type, callback) { docListeners[type] = callback; },
  };
  vm.runInNewContext(read('internal/web/static/theme.js'), {
    window, document, localStorage: storage,
    CustomEvent: class { constructor(type, options) { this.type = type; this.detail = options.detail; } },
  });
  return { window, root, controls, radios, values, storage, listeners, docListeners, events, media,
    systemChange(value) { media.matches = value; mediaChanged(); } };
}

test('appearance migrates legacy themes and validates stored preferences', () => {
  assert.equal(themeHarness({ theme: 'night' }).root.dataset.theme, 'dark');
  assert.equal(themeHarness({ theme: 'corporate' }).root.dataset.theme, 'light');
  const h = themeHarness({ theme: 'invalid', 'brine-accent': 'invalid', 'brine-density': 'invalid' }, { dark: true });
  assert.deepEqual(h.root.dataset, { theme: 'dark', accent: 'sage', density: 'comfortable' });
  assert.equal(h.controls['theme-select'].value, 'system');
  h.systemChange(false);
  assert.equal(h.root.dataset.theme, 'light');
  h.window.setBrineTheme('dark');
  h.systemChange(false);
  assert.equal(h.root.dataset.theme, 'dark');
  h.window.setBrineAccent('not-a-color');
  h.window.setBrineDensity('not-a-density');
  h.window.setBrineTheme('not-a-theme');
  assert.deepEqual(h.root.dataset, { theme: 'dark', accent: 'sage', density: 'comfortable' });
});

test('appearance updates controls, persists selections and resets only owned keys', () => {
  const h = themeHarness({ draft: 'unsaved source' });
  h.docListeners.change({ target: { id: 'theme-select', value: 'light' } });
  h.docListeners.change({ target: { id: 'density-select', value: 'compact' } });
  h.docListeners.change({ target: { value: 'rose', matches: () => true } });
  assert.deepEqual(h.root.dataset, { theme: 'light', accent: 'rose', density: 'compact' });
  assert.equal(h.values.get('brine-accent'), 'rose');
  assert.equal(h.values.get('brine-density'), 'compact');
  assert.equal(h.values.get('theme'), 'light');
  assert.deepEqual(h.radios.filter(r => r.checked).map(r => r.value), ['rose']);
  assert.equal(h.events.at(-1).type, 'brine:theme');
  assert.equal(h.events.at(-1).detail.density, 'compact');
  h.docListeners.click({ target: { closest: () => true } });
  assert.deepEqual([...h.values], [['draft', 'unsaved source']]);
  assert.deepEqual(h.root.dataset, { theme: 'light', accent: 'sage', density: 'comfortable' });
  assert.match(h.controls['preference-feedback'].textContent, /reset/);
});

test('appearance synchronizes local storage and system changes but ignores session storage', () => {
  const h = themeHarness();
  h.values.set('theme', 'dark');
  h.values.set('brine-accent', 'lilac');
  h.listeners.storage({ key: 'theme', storageArea: {} });
  assert.equal(h.root.dataset.accent, 'sage');
  h.listeners.storage({ key: 'unrelated', storageArea: h.storage });
  assert.equal(h.root.dataset.accent, 'sage');
  h.listeners.storage({ key: 'theme', storageArea: h.storage });
  assert.deepEqual(h.root.dataset, { theme: 'dark', accent: 'lilac', density: 'comfortable' });
  h.values.clear();
  h.listeners.storage({ key: null, storageArea: h.storage });
  h.systemChange(true);
  assert.deepEqual(h.root.dataset, { theme: 'dark', accent: 'sage', density: 'comfortable' });
  assert.equal(h.controls['theme-select'].value, 'system');
});

test('appearance still works in memory when storage reads or writes fail', () => {
  for (const options of [{ failReads: true }, { failWrites: true }]) {
    const h = themeHarness({}, options);
    h.window.setBrineTheme('dark');
    h.window.setBrineAccent('glacier');
    h.window.setBrineDensity('compact');
    h.docListeners.DOMContentLoaded();
    assert.deepEqual(h.root.dataset, { theme: 'dark', accent: 'glacier', density: 'compact' });
    assert.equal(h.controls['preference-storage-warning'].hidden, false);
    assert.match(h.controls['preference-feedback'].textContent, /this tab only/);
    h.window.resetBrinePreferences();
    assert.deepEqual(h.root.dataset, { theme: 'light', accent: 'sage', density: 'comfortable' });
  }
});

test('stream status handles markers split at every chunk boundary', () => {
  const { createStreamStatus } = require('../internal/web/static/ui.js');
  for (const marker of ['[brine:error]', '[brine:done]']) {
    for (let split = 1; split < marker.length; split++) {
      const stream = createStreamStatus();
      stream.push('normal output\n' + marker.slice(0, split));
      assert.equal(stream.status.error, false);
      assert.equal(stream.status.done, false);
      stream.push(marker.slice(split) + '\n');
      assert.equal(stream.status.error, marker === '[brine:error]');
      assert.equal(stream.status.done, marker === '[brine:done]');
    }
  }
});

test('new file fields assign untrusted names as DOM values, never HTML', () => {
  const { createFileFields } = require('../internal/web/static/ui.js');
  const doc = { createElement: () => ({ style: {}, setAttribute(name, value) { this[name] = value; } }) };
  const name = '\"><img src=x onerror=alert(1)>.pkl';
  const fields = createFileFields(doc, name);
  assert.equal(fields.input.value, name);
  assert.equal(fields.textarea['aria-label'], 'Contents of ' + name);
  assert.equal(fields.textarea.placeholder, '// ' + name);
  assert.equal(fields.input.innerHTML, undefined);
  assert.equal(fields.textarea.innerHTML, undefined);
});

test('editor completions follow the Dill 0.5 camelCase schema', () => {
  const source = read('internal/web/assets/editor.js');
  const termsBlock = source.match(/const pklTerms = \[([\s\S]*?)\];/);
  assert.ok(termsBlock, 'missing editor completion terms');
  const terms = [...termsBlock[1].matchAll(/'([^']+)'/g)].map(match => match[1]);
  for (const field of ['dependsOn', 'pullPolicy', 'containerName', 'networkMode', 'capAdd', 'hostIp', 'readOnly', 'externalVolume', 'startPeriod', 'memLimit', 'memReservation', 'cpus', 'cpuShares', 'pidsLimit']) {
    assert.ok(terms.includes(field), `missing Dill 0.5 field ${field}`);
  }
  for (const obsolete of ['depends_on', 'pull_policy', 'container_name', 'network_mode', 'cap_add', 'host_ip', 'read_only', 'external_volume', 'start_period', 'resources', 'working_dir', 'entrypoint', 'networks', 'secrets']) {
    assert.ok(!terms.includes(obsolete), `unsupported completion ${obsolete}`);
  }
});

test('built CSS retains every authored component variant for dynamic Go/HTMX fragments', () => {
  const source = read('internal/web/assets/app.css');
  const built = read('internal/web/static/vendor/app.css');
  for (const selector of new Set(source.match(/\.ui-[a-z][a-z0-9-]*/g))) {
    assert.ok(built.includes(selector), `missing ${selector}: rebuild assets and keep the full owned component API`);
  }
});

test('git actions refresh revision state and streamed push failure remains failed', async () => {
  const template = read('internal/web/templates/stacks/detail.html');
  assert.match(template, /hx-on::after-request="htmx\.trigger\(document\.body, 'git-refresh'\)"/);
  assert.match(template, /id="git-state-values"[^>]*hx-trigger="git-refresh from:body"[^>]*hx-select="#git-state-values"/);
  const script = template.slice(template.indexOf('async function runAction('), template.indexOf('</script>', template.indexOf('async function runAction(')));
  for (const [marker, expected] of [['[brine:error] push failed', 'failed'], ['[brine:done] git push complete', 'complete']]) {
    const elements = Object.fromEntries(['action-output', 'action-log', 'action-title', 'action-status'].map(id => [id, {textContent: '', classList: {remove() {}}, scrollTop: 0, scrollHeight: 0}]));
    const events = []; let readCount = 0;
    const context = {
      document: {body: {}, getElementById: id => elements[id] || null, querySelectorAll: () => []},
      htmx: {trigger: (_, name) => events.push(name)},
      fetch: async () => ({ok: true, body: {getReader: () => ({read: async () => readCount++ ? {done: true} : {done: false, value: new TextEncoder().encode(marker)}})}}),
      BrineUI: require('../internal/web/static/ui.js'), brineHeaders: headers => headers,
      showToast() {}, TextDecoder, Date,
    };
    vm.createContext(context); vm.runInContext(script, context);
    await context.runAction('git/push', 'test', 'POST', '{}', 'application/json');
    assert.equal(elements['action-status'].textContent, expected);
    assert.ok(events.includes('git-refresh'));
  }
});

test('log filtering is case-insensitive and can restore previously hidden lines', () => {
  const { filterLogLines } = require('../internal/web/static/ui.js');
  const lines = [{ textContent: 'Server ERROR' }, { textContent: 'server ready' }];
  const container = { children: lines };
  filterLogLines(container, 'error');
  assert.deepEqual(lines.map(line => line.hidden), [false, true]);
  const incoming = { textContent: 'another ERROR' };
  filterLogLines({ children: [incoming] }, 'error');
  assert.equal(incoming.hidden, false);
  const nonmatching = { textContent: 'all clear' };
  filterLogLines({ children: [nonmatching] }, 'error');
  assert.equal(nonmatching.hidden, true);
  filterLogLines(container, '');
  assert.deepEqual(lines.map(line => line.hidden), [false, false]);
});

test('container log EventSource reports and closes terminal states', () => {
  const source = read('internal/web/templates/containers/detail.html').match(/<script>([\s\S]*?)<\/script>/)[1];
  function harness() {
    const elements = {
      'log-container': {
        children: [], scrollHeight: 0,
        insertAdjacentHTML(_position, html) {
          this.lastElementChild = { textContent: html };
          this.children.push(this.lastElementChild);
        },
      },
      'follow-toggle': { checked: true },
      'tail-select': { value: '100' },
      'log-status': {},
      'log-filter': { value: '' },
    };
    for (const element of Object.values(elements)) element.addEventListener = () => {};
    const sources = [];
    class EventSource {
      static CLOSED = 2;
      constructor(url) { this.url = url; this.listeners = {}; this.closed = false; sources.push(this); }
      addEventListener(name, callback) { this.listeners[name] = callback; }
      close() { this.closed = true; this.readyState = EventSource.CLOSED; }
    }
    vm.runInNewContext(source, {
      document: { getElementById: id => elements[id] }, EventSource,
      MutationObserver: class { observe() {} },
      BrineUI: require('../internal/web/static/ui.js'),
    });
    return { status: elements['log-status'], stream: sources[0], elements };
  }
  for (const [event, text, css] of [
    ['done', 'ended', 'ui-status-neutral'],
    ['stream-error', 'stream failed — reload to retry', 'ui-status-error'],
  ]) {
    const { status, stream } = harness();
    assert.equal(status.textContent, 'connecting');
    assert.match(stream.url, /tail=100$/);
    stream.onopen();
    assert.equal(status.textContent, 'live');
    assert.match(status.className, /ui-status-running/);
    stream.listeners[event]({ data: '' });
    assert.equal(status.textContent, text);
    assert.ok(status.className.includes(css));
    assert.equal(stream.closed, true);
  }
  const { status, stream } = harness();
  stream.onopen();
  stream.onerror();
  assert.equal(status.textContent, 'reconnecting');
  assert.match(status.className, /ui-status-queued/);
  assert.equal(stream.closed, false);
  const incoming = harness();
  incoming.elements['log-filter'].value = 'error';
  incoming.stream.onmessage({ data: 'all clear' });
  assert.equal(incoming.elements['log-container'].lastElementChild.hidden, true);
});
