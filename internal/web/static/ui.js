// Helpers shared by the server-rendered pages and their regression tests.
(function (root) {
  function createStreamStatus() {
    let tail = '';
    const status = { error: false, done: false };
    return {
      status,
      push(chunk) {
        const text = tail + chunk;
        if (text.includes('[brine:error]')) status.error = true;
        if (text.includes('[brine:done]')) status.done = true;
        tail = text.slice(-12);
      },
    };
  }

  function createFileFields(doc, name) {
    const input = doc.createElement('input');
    input.type = 'hidden';
    input.name = 'filename';
    input.value = name;
    const textarea = doc.createElement('textarea');
    textarea.name = 'filecontent';
    textarea.className = 'ui-textarea font-mono text-sm w-full resize-y';
    textarea.style.minHeight = 'var(--editor-new-min-height)';
    textarea.placeholder = '// ' + name;
    textarea.setAttribute('aria-label', 'Contents of ' + name);
    return { input, textarea };
  }

  function duration(name, fallback) {
    if (!root.document || typeof root.getComputedStyle !== 'function') return fallback;
    const value = root.getComputedStyle(root.document.documentElement).getPropertyValue(name).trim();
    const match = /^(\d+(?:\.\d+)?)(ms|s)$/.exec(value);
    if (!match) return fallback;
    const milliseconds = Number(match[1]) * (match[2] === 's' ? 1000 : 1);
    return Number.isFinite(milliseconds) ? milliseconds : fallback;
  }

  const api = { createStreamStatus, createFileFields, duration };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.BrineUI = api;
})(typeof window === 'undefined' ? globalThis : window);
