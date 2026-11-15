const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');

class Node {
  constructor() { this.children = []; this.dataset = {}; this.classList = {remove(){}}; this.textContent = ''; this.disabled = false; this.checked = false; }
  appendChild(n) { this.children.push(n); return n; }
  replaceChildren() { this.children = []; }
  addEventListener(_, cb) { this.click = cb; }
}
test('attended review retains 256 literal rows and never auto-confirms', async () => {
  const output = new Node(); output.parentNode = new Node();
  const log = new Node(), title = new Node();
  const ids = {'action-log': log, 'action-title': title};
  const document = {getElementById(id) { return ids[id] || null; }, createElement() { return new Node(); }, createTextNode(text) { const n = new Node(); n.textContent = text; return n; }};
  const rows = Array.from({length: 256}, (_, i) => ({kind: 'container', name: i === 255 ? '<literal-name>' : 'resource-' + i, identityKind: 'planned-name'}));
  let calls = 0;
  const context = {document, BrineUI: {operationLookup(){}}, brineHeaders: x => x, fetch: async () => {
    calls++; return {ok: true, headers: {get: () => 'br_' + 'a'.repeat(32)}, json: async () => ({requestId: 'br_' + 'a'.repeat(32), reviewDigest: 'digest', confirmationToken: 'token', review: {rows}})};
  }};
  vm.createContext(context);
  vm.runInContext(fs.readFileSync('internal/web/static/attended.js', 'utf8'), context);
  await context.attendedReview('up', 'demo', output);
  assert.equal(calls, 1);
  assert.match(log.textContent, /<literal-name>/);
  assert.match(log.textContent, /resource-254/);
  const controls = output.children[0];
  await controls.children[1].click();
  assert.equal(calls, 1, 'unchecked confirmation must not issue POST');
});
