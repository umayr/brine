// Helpers shared by the server-rendered pages and their regression tests.
(function (root) {
  function createStreamStatus(options = {}) {
    let tail = '', pending = '', received = false, invalid = false, ended = false;
    const status = { error: false, done: false, result: 'unknown', invocation: null };
    const qualified = options.qualified === true;
    const digest = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
    const keys = (value, required, optional = []) => value && typeof value === 'object' && !Array.isArray(value) &&
      required.every(key => Object.hasOwn(value, key)) && Object.keys(value).every(key => required.includes(key) || optional.includes(key));
    const effects = ['none', 'complete', 'partial', 'unknown'];
    function validInvocation(value) {
      if (!keys(value, ['receipt', 'exitCode', 'status']) || !Number.isInteger(value.exitCode) || value.exitCode < -1 || value.exitCode > 255 ||
          !['succeeded', 'changes', 'failed', 'cancelled', 'unknown'].includes(value.status)) return false;
      const r = value.receipt;
      if (r === null) return value.status === 'unknown';
      if (!keys(r, ['version', 'operationId', 'operation', 'target', 'stack', 'outcome', 'effects', 'changesDetected', 'resources', 'resourcesTruncated', 'errors']) ||
          r.version !== 'dill.operation/1' || !/^op_[a-f0-9]{32}$/.test(r.operationId) || r.operation !== options.operation || (r.stack !== options.stack && !(r.target === null && r.stack === '')) ||
          !['succeeded', 'changes', 'invalid_input', 'failed', 'cancelled', 'unknown'].includes(r.outcome) || !effects.includes(r.effects) ||
          typeof r.changesDetected !== 'boolean' || typeof r.resourcesTruncated !== 'boolean' || !Array.isArray(r.resources) || r.resources.length > 4096 ||
          !Array.isArray(r.errors) || r.errors.length > 16) return false;
      if (r.target !== null && (!keys(r.target, ['engine', 'endpointDigest'], ['qualifiedIdentityDigest']) || r.target.engine !== 'docker' || !digest(r.target.endpointDigest) ||
          (Object.hasOwn(r.target, 'qualifiedIdentityDigest') && !digest(r.target.qualifiedIdentityDigest)))) return false;
      if (r.target === null && (r.stack !== '' || r.effects !== 'none' || r.changesDetected || r.resources.length || !['invalid_input','failed'].includes(r.outcome))) return false;
      const readOnly = ['validate', 'plan', 'diff'].includes(r.operation);
      if (!readOnly && (r.effects !== 'none' || r.outcome === 'succeeded') && !digest(r.target?.qualifiedIdentityDigest)) return false;
      if (!r.resources.every(item => keys(item, ['kind', 'identityDigest', 'action', 'effect']) &&
          ['service', 'container', 'image', 'network', 'volume', 'createScope'].includes(item.kind) && digest(item.identityDigest) &&
          typeof item.action === 'string' && ({service:['create','recreate','remove','noop'],container:['stop','start','remove','intent.stopRemove','intent.removeStoppedCandidate'],createScope:['create'],network:['ensure','intent.retain'],volume:['ensure','intent.retain'],image:['pull','remove','intent.removeCandidate']}[item.kind] || []).includes(item.action) && ['none','complete','unknown'].includes(item.effect) && ((item.kind === 'service' || item.action.startsWith('intent.')) ? item.effect === 'none' : item.effect !== 'none'))) return false;
      // A receipt is an observation, not an inventory: omitted/truncated effects
      // may be more conservative than the visible prefix, never less conservative.
      if (r.resources.some(item => item.effect === 'unknown') && r.effects !== 'unknown') return false;
      if (r.resources.some(item => item.effect === 'complete') && r.effects === 'none') return false;
      if (readOnly && r.effects !== 'none' && !(r.outcome === 'unknown' && r.effects === 'unknown')) return false;
      if (r.operation === 'validate' && r.resources.length !== 0) return false;
      if (['plan', 'diff'].includes(r.operation) && !r.resources.every(item => item.kind === 'service' && item.effect === 'none')) return false;
      if (!r.errors.every(item => keys(item, ['code', 'message']) && typeof item.code === 'string' && /^[A-Z_]{1,80}$/.test(item.code) &&
          typeof item.message === 'string' && item.message.length <= 256 && !/[\x00-\x1f]/.test(item.message))) return false;
      if (value.status === 'succeeded') return value.exitCode === 0 && r.outcome === 'succeeded' && ['none', 'complete'].includes(r.effects) && r.errors.length === 0 && (readOnly || digest(r.target?.qualifiedIdentityDigest));
      if (value.status === 'changes') return r.outcome === 'changes' && ['plan', 'diff'].includes(r.operation) && r.changesDetected && r.effects === 'none' && value.exitCode === (r.operation === 'plan' ? 1 : 0) && r.errors.length === 0;
      if (value.status === 'cancelled') return r.outcome === 'cancelled' && [0, 1].includes(value.exitCode);
      if (value.status === 'unknown') return r.outcome === 'unknown' && r.effects === 'unknown' && value.exitCode === 1;
      if (r.outcome === 'succeeded') return ['none', 'complete'].includes(r.effects) && value.exitCode === 1 && r.errors.length > 0;
      if (r.outcome === 'invalid_input') return value.exitCode === 2;
      return r.outcome === 'failed' && value.exitCode === 1;
    }
    function uniqueKeys(raw) {
      const stack = [];
      for (let i = 0; i < raw.length; i++) {
        const c = raw[i];
        if (c === '{' || c === '[') stack.push(c === '{' ? new Set() : null);
        else if (c === '}' || c === ']') stack.pop();
        else if (c === '"') {
          const start = i++;
          while (i < raw.length) { if (raw[i] === '\\') i += 2; else if (raw[i] === '"') break; else i++; }
          let next = i + 1;
          while (/\s/.test(raw[next] || '') && next < raw.length) next++;
          if (raw[next] === ':' && stack.at(-1) instanceof Set) {
            const key = JSON.parse(raw.slice(start, i + 1));
            if (stack.at(-1).has(key)) return false;
            stack.at(-1).add(key);
          }
        }
      }
      return true;
    }
    function line(text) {
      if (ended && text.trim()) { invalid = true; return; }
      if (text.startsWith('[brine:operation] ')) {
        if (received || ended) { invalid = true; return; }
        received = true;
        try {
          const raw = text.slice(18);
          const value = JSON.parse(raw);
          if (!uniqueKeys(raw) || !validInvocation(value)) { invalid = true; return; }
          status.invocation = value;
        } catch { invalid = true; }
      } else if (text.startsWith('[brine:done]')) { status.done = true; ended = true; }
      else if (text.startsWith('[brine:error]')) { status.error = true; ended = true; }
      else if (text.trim()) invalid = true; // never display subprocess or unexpected text
    }
    return {
      status,
      push(chunk) {
        if (!qualified) {
          const text = tail + chunk;
          if (text.includes('[brine:error]')) status.error = true;
          if (text.includes('[brine:done]')) status.done = true;
          tail = text.slice(-12);
          return;
        }
        pending += chunk;
        if (pending.length > 2 * 1024 * 1024 + 4096) { invalid = true; pending = ''; return; }
        let newline;
        while ((newline = pending.indexOf('\n')) !== -1) {
          line(pending.slice(0, newline));
          pending = pending.slice(newline + 1);
        }
      },
      finish() {
        if (!qualified) return;
        if (pending.trim() || invalid || !received || !ended || !status.invocation) {
          status.result = 'unknown'; status.error = true; return;
        }
        status.result = status.invocation.status;
        if (status.result === 'succeeded' && (status.error || !status.done)) status.result = 'failed';
      },
      summary() {
        const value = status.invocation, r = value?.receipt;
        const lines = ['Invocation: ' + status.result, 'Current action only; not durable history. Copy operation ID before leaving. Reload/disconnect never authorizes replay.'];
        if (r) {
          lines.push('Operation ID: ' + r.operationId, 'Outcome: ' + r.outcome, 'Effects: ' + r.effects,
            'Process exit: ' + value.exitCode, 'Endpoint selector digest (not daemon identity): ' + (r.target?.endpointDigest || 'unavailable'),
            'Qualified identity digest (received): ' + (r.target?.qualifiedIdentityDigest || 'not received'));
          for (const item of r.resources) lines.push(item.kind + ' · ' + item.action + ' · ' + item.effect + ' · ' + item.identityDigest);
          if (r.resourcesTruncated) lines.push('Resource observations truncated; not a complete inventory.');
          for (const item of r.errors) lines.push(item.code + ': ' + item.message);
        } else lines.push('No validated terminal receipt; effects are unknown.');
        if (status.result !== 'succeeded') lines.push('Do not retry or infer rollback. Inspect offline coordination state and actual resources before exact operator acknowledgement.');
        return lines.join('\n');
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

  function filterLogLines(container, query) {
    const needle = query.toLowerCase();
    for (const line of container.children) {
      line.hidden = !line.textContent.toLowerCase().includes(needle);
    }
  }

  function duration(name, fallback) {
    if (!root.document || typeof root.getComputedStyle !== 'function') return fallback;
    const value = root.getComputedStyle(root.document.documentElement).getPropertyValue(name).trim();
    const match = /^(\d+(?:\.\d+)?)(ms|s)$/.exec(value);
    if (!match) return fallback;
    const milliseconds = Number(match[1]) * (match[2] === 's' ? 1000 : 1);
    return Number.isFinite(milliseconds) ? milliseconds : fallback;
  }

  const api = { createStreamStatus, createFileFields, filterLogLines, duration };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.BrineUI = api;
})(typeof window === 'undefined' ? globalThis : window);
